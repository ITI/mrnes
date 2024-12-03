package mrnes

// nets.go contains code and data structures supporting the
// simulation of traffic through the communication network.
// mrnes supports passage of discrete packets.  These pass
// through the interfaces of devices (routers and switch) on the
// shortest path route between endpoints, accumulating delay through the interfaces
// and across the networks, as a function of overall traffic load (most particularly
// including the background flows)
import (
	"fmt"
	"github.com/iti/evt/evtm"
	"github.com/iti/evt/vrtime"
	"github.com/iti/rngstream"
	"golang.org/x/exp/slices"
	"gopkg.in/yaml.v3"
	"math"
	"strconv"
	"strings"
)

// The mrnsbit network simulator is built around two strong assumptions that
// simplify implementation, but which may have to be addressed if mrnsbit
// is to be used when fine-grained networking details are thought to be important.
//
// The first assumption is that routing is static, that the complete route for
// a message from specified source to specified destination can be computed at
// the time the message enters the network.   It happens that this implementation
// uses minimum hop count as the metric for choosing routes, but this is not so rigourously
// embedded in the design as is static routes.
//
// The second assumption is related to the reality that messages do not traverse a link or
// pass through an interface instaneously, they 'connect' across links, through networks, and through devices.
// Flows have bandwidth, and every interface, link, device, and network has its own bandwidth
// limit.  From the point of view of the receiving endpt, the effective bandwidth (assuming the bandwidths
// all along the path don't change) is the minimum bandwidth among all the things on the path.
// In the path before the hop that induces the least bandwidth message may scoot through connections
// at higher bandwidth, but the progress of that connection is ultimately limited  by the smallest bandwidth.
// The simplifying assumption then is that the connection of the message's bits _everywhere_ along the path
// is at that minimum bandwidth.   More detail in tracking and changing bandwidths along sections of the message
// are possible, but at this point it is more complicated and time-consuming to do that than it seems to be worth
// for anticipated use-cases.
//

// intPair, intrfcIDPair, intrfcRate and floatPair are
// structs introduced to add more than basic elements to lists and maps
type intPair struct {
	i, j int
}

type intrfcIDPair struct {
	prevID, nextID int
	rate           float64
}

type intrfcRate struct {
	intrfcID int
	rate     float64
}

type floatPair struct {
	x, y float64
}

// classQueue holds information about a given
// class of packets and/or flows
type classQueue struct {
	classID       int
	ingressLambda float64       // sum of rates of flows in this class on the ingress side
	egressLambda  float64       // sum of rates of flows in this class on the egress side
	inQueue       []*NetworkMsg // number of enqueued packets
	waiting       float64       // waiting time of class flow (from priority queue formula)
}

// append the given NetworkMsg to the classQueue, to await service
func (cQ *classQueue) addNetworkMsg(nm *NetworkMsg) {
	cQ.inQueue = append(cQ.inQueue, nm)
}

// represent the queue state of the classQueue in a string, for traces
func (cQ *classQueue) Str() string {
	rtnVec := []string{strconv.Itoa(cQ.classID), strconv.Itoa(len(cQ.inQueue))}
	return strings.Join(rtnVec, " % ")
}

// ClassQueue holds multi-level priority queue structures for
// an interface
type ClassQueue struct {
	ingressID2Q map[int]int   // classID to index in ordered priority list
	egressID2Q  map[int]int   // classID to index in ordered priority list
	ingressQs   []*classQueue // messages waiting for service on the ingress side, earlier indices mean higher priority
	egressQs    []*classQueue // messages waiting for service on the egress side, earlier indices mean higher priority
}

// createClassQueue is a constructor
func createClassQueue() *ClassQueue {
	CQ := new(ClassQueue)
	CQ.ingressID2Q = make(map[int]int)
	CQ.egressID2Q = make(map[int]int)
	CQ.ingressQs = []*classQueue{}
	CQ.egressQs = []*classQueue{}
	return CQ
}

// Str represents the state of the ClassQueue queues as a string, for traces
func (CQ *ClassQueue) Str() string {
	rtn := []string{}
	for idx := 0; idx < len(CQ.ingressQs); idx++ {
		rtn = append(rtn, "ingress_"+CQ.ingressQs[idx].Str())
	}

	for idx := 0; idx < len(CQ.egressQs); idx++ {
		rtn = append(rtn, "egress_"+CQ.egressQs[idx].Str())
	}

	return strings.Join(rtn, ",")
}

// append a classID to the ClassQueue if not already present
// maintain decreasing order of classID (higher classID is higher priority)
// in the insertion
func (CQ *ClassQueue) addClassID(classID int, ingress bool) {
	var Qs []*classQueue
	var id2Q map[int]int

	// do processing on queues and id2A
	if ingress {
		Qs = CQ.ingressQs
		id2Q = CQ.ingressID2Q
	} else {
		Qs = CQ.egressQs
		id2Q = CQ.egressID2Q
	}

	// if we have it already we're done
	_, present := id2Q[classID]
	if present {
		return
	}

	// starting with highest priority, find first instance of lower priority.
	// That's the insertion point
	here := 0
	for idx := 0; idx < len(Qs); idx++ {
		thisClassID := Qs[idx].classID
		if thisClassID < classID {
			break
		}
		here += 1
	}

	// create and initialize the new classQueue structure
	newcq := new(classQueue)
	newcq.classID = classID
	newcq.inQueue = []*NetworkMsg{}

	// create newQs to be the modified slice
	newQs := Qs[:here]
	newQs = append(newQs, newcq)
	Qs = append(newQs, Qs[here:]...)

	// redo the id2Q map after the insert
	id2Q = make(map[int]int)
	for idx := 0; idx < len(Qs); idx++ {
		cq := Qs[idx]
		id2Q[cq.classID] = idx
	}

	// save the modified data structures
	if ingress {
		CQ.ingressQs = Qs
		CQ.ingressID2Q = id2Q
	} else {
		CQ.egressQs = Qs
		CQ.egressID2Q = id2Q
	}
}

// transferBW computes how long it taks a message with the given msgLen to traverse the interface, once in motion
func (CQ *ClassQueue) transferBW(classID int, msgLen float64, intrfc *intrfcStruct, ingress bool) float64 {
	bndwdth := intrfc.useableBW()

	// subtract off bandwidth of higher priority flows
	var qIdx int
	if ingress {
		qIdx = CQ.ingressID2Q[classID]
	} else {
		qIdx = CQ.egressID2Q[classID]
	}

	// subtract off bandwidth of all classIDs with higher priority
	for idx := 0; idx < qIdx; idx++ {
		if ingress {
			bndwdth -= CQ.ingressQs[idx].ingressLambda
		} else {
			bndwdth -= CQ.egressQs[idx].egressLambda
		}
	}
	return bndwdth
}

// addNetworkMsg appends the network message argument to the correct
// inQueue
func (CQ *ClassQueue) addNetworkMsg(nm *NetworkMsg, ingress bool) {
	classID := nm.ClassID
	if ingress {
		CQ.ingressQs[CQ.ingressID2Q[classID]].inQueue = append(CQ.ingressQs[CQ.ingressID2Q[classID]].inQueue, nm)
	} else {
		CQ.egressQs[CQ.egressID2Q[classID]].inQueue = append(CQ.egressQs[CQ.egressID2Q[classID]].inQueue, nm)
	}
}

// nxtNetworkMsg is called to extract first message with highest priority classID
// from its inQueue and compute its delay through the interface
func (CQ *ClassQueue) nxtNetworkMsg(intrfc *intrfcStruct, ingress bool) *NetworkMsg {
	// find the highest priority message to move
	var nm *NetworkMsg
	var Qs []*classQueue

	if ingress {
		Qs = CQ.ingressQs
	} else {
		Qs = CQ.egressQs
	}

	// look for the highest priority queue that is not empty
	for idx := 0; idx < len(Qs); idx++ {
		cg := Qs[idx]
		if len(cg.inQueue) > 0 {
			// found it.  Extract it, modify its queue
			nm, cg.inQueue = cg.inQueue[0], cg.inQueue[1:]
			break
		}
	}
	return nm
}

// getClassQueue returns the classQueue associated with the given classID
func (CQ *ClassQueue) getClassQueue(classID int, ingress bool) *classQueue {
	if ingress {
		_, present := CQ.ingressID2Q[classID]
		if !present {
			panic(fmt.Errorf("classID %d not found in ClassQueue", classID))
		}
		return CQ.ingressQs[CQ.ingressID2Q[classID]]
	} else {
		_, present := CQ.egressID2Q[classID]
		if !present {
			panic(fmt.Errorf("classID %d not found in ClassQueue", classID))
		}
		return CQ.egressQs[CQ.egressID2Q[classID]]
	}
}

// putClassQueue replaces the classQueue element
func (CQ *ClassQueue) putClassQueue(cQ *classQueue, ingress bool) {
	if ingress {
		_, present := CQ.ingressID2Q[cQ.classID]
		if !present {
			panic(fmt.Errorf("classID %d not found in ClassQueue", cQ.classID))
		}
		CQ.ingressQs[CQ.ingressID2Q[cQ.classID]] = cQ
	} else {
		_, present := CQ.egressID2Q[cQ.classID]
		if !present {
			panic(fmt.Errorf("classID %d not found in ClassQueue", cQ.classID))
		}
		CQ.egressQs[CQ.egressID2Q[cQ.classID]] = cQ
	}
}

// NetworkMsgType give enumeration for message types that may be given to the network
// to carry.  packet is a discrete packet, handled differently from flows.
// srtFlow tags a message that introduces a new flow, endFlow tags one that terminates it,
// and chgFlow tags a message that alters the flow rate on a given flow.
type NetworkMsgType int

const (
	PacketType NetworkMsgType = iota
	FlowType
)

// FlowAction describes the reason for the flow message, that it is starting, ending, or changing the request rate
type FlowAction int

const (
	None FlowAction = iota
	Srt
	Chg
	End
)

// nmtToStr is a translation table for creating strings from more complex
// data types
var nmtToStr map[NetworkMsgType]string = map[NetworkMsgType]string{PacketType: "packet",
	FlowType: "flow"}

// routeStepIntrfcs maps a pair of device IDs to a pair of interface IDs
// that connect them
var routeStepIntrfcs map[intPair]intPair

// getRouteStepIntrfcs looks up the identity of the interfaces involved in connecting
// the named source and the named destination.  These were previously built into a table
func getRouteStepIntrfcs(srcID, dstID int) (int, int) {
	ip := intPair{i: srcID, j: dstID}
	intrfcs, present := routeStepIntrfcs[ip]
	if !present {
		intrfcs, present = routeStepIntrfcs[intPair{j: srcID, i: dstID}]
		if !present {
			panic(fmt.Errorf("no step between %s and %s", TopoDevByID[srcID].DevName(), TopoDevByID[dstID].DevName()))
		}
		return intrfcs.j, intrfcs.i
	}
	return intrfcs.i, intrfcs.j
}

// NetworkPortal implements the pces interface used to pass
// traffic between the application layer and the network sim
type NetworkPortal struct {
	QkNetSim      bool
	ReturnTo      map[int]*rtnRecord // indexed by connectID
	LossRtn       map[int]*rtnRecord // indexed by connectID
	ReportRtnSrc  map[int]*rtnRecord // indexed by connectID
	ReportRtnDst  map[int]*rtnRecord // indexed by connectID
	RequestRate   map[int]float64    // indexed by flowID to get requested arrival rate
	AcceptedRate  map[int]float64    // indexed by flowID to get accepted arrival rate
	Class         map[int]int        // indexed by flowID to get priority class
	Elastic       map[int]bool       // indexed by flowID to record whether flow is elastic
	Connections   map[int]int        // indexed by connectID to get flowID
	InvConnection map[int]int        // indexed by flowID to get connectID
	LatencyConsts map[int]float64    // indexex by flowID to get latency constants on flow's route
}

// ActivePortal remembers the most recent NetworkPortal created
// (there should be only one call to CreateNetworkPortal...)
var ActivePortal *NetworkPortal

type ActiveRec struct {
	Number int
	Rate   float64
}

// CreateNetworkPortal is a constructor, passed a flag indicating which
// of two network simulation modes to use, passes a flag indicating whether
// packets should be passed whole, and writes the NetworkPortal pointer into a global variable
func CreateNetworkPortal() *NetworkPortal {
	if ActivePortal != nil {
		return ActivePortal
	}
	np := new(NetworkPortal)

	// set default settings
	np.QkNetSim = false
	np.ReturnTo = make(map[int]*rtnRecord)
	np.LossRtn = make(map[int]*rtnRecord)
	np.ReportRtnSrc = make(map[int]*rtnRecord)
	np.ReportRtnDst = make(map[int]*rtnRecord)
	np.RequestRate = make(map[int]float64)
	np.AcceptedRate = make(map[int]float64)
	np.Class = make(map[int]int)
	np.Elastic = make(map[int]bool)
	np.Connections = make(map[int]int)
	np.InvConnection = make(map[int]int)
	np.LatencyConsts = make(map[int]float64)

	// save the mrnes memory space version
	ActivePortal = np
	connections = 0
	return np
}

// SetQkNetSim saves the argument as indicating whether latencies
// should be computed as 'Placed', meaning constant, given the state of the network at the time of
// computation
func (np *NetworkPortal) SetQkNetSim(quick bool) {
	np.QkNetSim = quick
}

// ClearRmFlow removes entries from maps indexed
// by flowID and associated connectID, to help clean up space
func (np *NetworkPortal) ClearRmFlow(flowID int) {
	connectID := np.InvConnection[flowID]
	delete(np.ReturnTo, connectID)
	delete(np.LossRtn, connectID)
	delete(np.ReportRtnSrc, connectID)
	delete(np.ReportRtnDst, connectID)
	delete(np.Connections, connectID)

	delete(np.RequestRate, flowID)
	delete(np.AcceptedRate, flowID)
	delete(np.Class, flowID)
	delete(np.LatencyConsts, flowID)
}

// EndptDevModel helps NetworkPortal implement the pces NetworkPortal interface,
// returning the CPU model associated with a named endpt.  Present because the
// application layer does not otherwise have visibility into the network topology
func (np *NetworkPortal) EndptDevModel(devName string, accelName string) string {
	endpt, present := EndptDevByName[devName]
	if !present {
		return ""
	}
	if len(accelName) == 0 {
		return endpt.EndptModel
	}
	accelModel, present := endpt.EndptAccelModel[accelName]
	if !present {
		return ""
	}
	return accelModel
}

// Depart is called to return an application message being carried through
// the network back to the application layer
func (np *NetworkPortal) Depart(evtMgr *evtm.EventManager, nm NetworkMsg) {
	connectID := nm.ConnectID

	// may not require knowledge that delivery made it
	rtnRec, present := np.ReturnTo[connectID]
	if !present || rtnRec == nil || rtnRec.rtnCxt == nil {
		return
	}

	rtnRec.prArrvl *= nm.PrArrvl
	rtnRec.pckts -= 1

	// if rtnRec.pckts is not zero there are more packets coming associated
	// with connectID and so we exit
	if rtnRec.pckts > 0 {
		return
	}

	prArrvl := rtnRec.prArrvl

	// so we can return now
	rtnMsg := new(RtnMsgStruct)
	rtnMsg.Latency = evtMgr.CurrentSeconds() - nm.StartTime
	if nm.carriesPckt() {
		rtnMsg.Rate = nm.PcktRate
		rtnMsg.PrLoss = (1.0 - prArrvl)
	} else {
		rtnMsg.Rate = np.AcceptedRate[nm.FlowID]
	}

	rtnMsg.Msg = nm.Msg

	rtnCxt := rtnRec.rtnCxt
	rtnFunc := rtnRec.rtnFunc

	// schedule the re-integration into the application simulator
	evtMgr.Schedule(rtnCxt, rtnMsg, rtnFunc, vrtime.SecondsToTime(0.0))

	delete(np.ReturnTo, connectID)
	delete(np.LossRtn, connectID)
	delete(np.ReportRtnSrc, connectID)
	delete(np.ReportRtnDst, connectID)
}

// requestedLoadFracVec computes the relative requested rate for a flow
// among a list of flows.   Used to rescale accepted rates
func (np *NetworkPortal) requestedLoadFracVec(vec []int) []float64 {
	rtn := make([]float64, len(vec))
	var agg float64

	// gather up the rates in an array and compute the normalizing sum
	for idx, flowID := range vec {
		rate := np.RequestRate[flowID]
		agg += rate
		rtn[idx] = rate
	}

	// normalize
	for idx := range vec {
		rtn[idx] /= agg
	}
	return rtn
}

// RtnMsgStruct formats the report passed from the network to the
// application calling it
type RtnMsgStruct struct {
	Latency float64 // span of time (secs) from srcDev to dstDev
	Rate    float64 // for a flow, its accept rate.  For a packet, the minimum non-flow bandwidth at a
	// network or interface it encountered
	PrLoss float64 // estimated probability of having been dropped somewhere in transit
	Msg    any     // msg introduced at EnterNetwork
}

// Arrive is called at the point an application message is received by the network
// and a new connectID is created (and returned) to track it.  It saves information needed to re-integrate
// the application message into the application layer when the message arrives at its destination
func (np *NetworkPortal) Arrive(rtns RtnDescs, frames int) int {

	// record how to transition from network to upper layer, through event handler at upper layer
	rtnRec := &rtnRecord{rtnCxt: rtns.Rtn.Cxt, rtnFunc: rtns.Rtn.EvtHdlr, prArrvl: 1.0, pckts: frames}
	connectID := nxtConnectID()
	np.ReturnTo[connectID] = rtnRec

	// if requested, record how to notify source end of connection at upper layer through connection
	if rtns.Src != nil {
		rtnRec = new(rtnRecord)
		*rtnRec = rtnRecord{rtnCxt: rtns.Src.Cxt, rtnFunc: rtns.Src.EvtHdlr, prArrvl: 1.0, pckts: 1}
		np.ReportRtnSrc[connectID] = rtnRec
	}

	// if requested, record how to notify destination end of connection at upper layer through event handler
	if rtns.Dst != nil {
		rtnRec = new(rtnRecord)
		*rtnRec = rtnRecord{rtnCxt: rtns.Dst.Cxt, rtnFunc: rtns.Dst.EvtHdlr, prArrvl: 1.0, pckts: 1}
		np.ReportRtnDst[connectID] = rtnRec
	}

	// if requested, record how to notify occurance of loss at upper layer, through event handler
	if rtns.Loss != nil {
		rtnRec = new(rtnRecord)
		*rtnRec = rtnRecord{rtnCxt: rtns.Loss.Cxt, rtnFunc: rtns.Loss.EvtHdlr, prArrvl: 1.0, pckts: 1}
		np.LossRtn[connectID] = rtnRec
	}
	return connectID
}

// lostConnection is called when a connection is lost by the network layer.
// The response is to remove the connection from the portal's table, and
// call the event handler passed in to deal with lost connections
func (np *NetworkPortal) lostConnection(evtMgr *evtm.EventManager, nm *NetworkMsg, connectID int) {
	_, present := np.ReturnTo[connectID]
	if !present {
		return
	}

	_, present = np.LossRtn[connectID]
	if !present {
		return
	}

	lossRec := np.LossRtn[connectID]
	lossCxt := lossRec.rtnCxt
	lossFunc := lossRec.rtnFunc

	// schedule the re-integration into the application simulator
	evtMgr.Schedule(lossCxt, nm.Msg, lossFunc, vrtime.SecondsToTime(0.0))

}

// DevCode is the base type for an enumerated type of network devices
type DevCode int

const (
	EndptCode DevCode = iota
	SwitchCode
	RouterCode
	UnknownCode
)

// DevCodeFromStr returns the devCode corresponding to an string name for it
func DevCodeFromStr(code string) DevCode {
	switch code {
	case "Endpt", "endpt":
		return EndptCode
	case "Switch", "switch":
		return SwitchCode
	case "Router", "router", "rtr":
		return RouterCode
	default:
		return UnknownCode
	}
}

// DevCodeToStr returns a string corresponding to an input devCode for it
func DevCodeToStr(code DevCode) string {
	switch code {
	case EndptCode:
		return "Endpt"
	case SwitchCode:
		return "Switch"
	case RouterCode:
		return "Router"
	case UnknownCode:
		return "Unknown"
	}

	return "Unknown"
}

// NetworkScale is the base type for an enumerated type of network type descriptions
type NetworkScale int

const (
	LAN NetworkScale = iota
	WAN
	T3
	T2
	T1
	GeneralNet
)

// NetScaleFromStr returns the networkScale corresponding to an string name for it
func NetScaleFromStr(netScale string) NetworkScale {
	switch netScale {
	case "LAN":
		return LAN
	case "WAN":
		return WAN
	case "T3":
		return T3
	case "T2":
		return T2
	case "T1":
		return T1
	default:
		return GeneralNet
	}
}

// NetScaleToStr returns a string name that corresponds to an input networkScale
func NetScaleToStr(ntype NetworkScale) string {
	switch ntype {
	case LAN:
		return "LAN"
	case WAN:
		return "WAN"
	case T3:
		return "T3"
	case T2:
		return "T2"
	case T1:
		return "T1"
	case GeneralNet:
		return "GeneralNet"
	default:
		return "GeneralNet"
	}
}

// NetworkMedia is the base type for an enumerated type of comm network media
type NetworkMedia int

const (
	Wired NetworkMedia = iota
	Wireless
	UnknownMedia
)

// NetMediaFromStr returns the networkMedia type corresponding to the input string name
func NetMediaFromStr(media string) NetworkMedia {
	switch media {
	case "Wired", "wired":
		return Wired
	case "wireless", "Wireless":
		return Wireless
	default:
		return UnknownMedia
	}
}

// every new network connection is given a unique connectID upon arrival
var connections int

func nxtConnectID() int {
	connections += 1
	return connections
}

type DFS map[int]intrfcIDPair

// TopoDev interface specifies the functionality different device types provide
type TopoDev interface {
	DevName() string              // every device has a unique name
	DevID() int                   // every device has a unique integer id
	DevType() DevCode             // every device is one of the devCode types
	DevIntrfcs() []*intrfcStruct  // we can get from devices a list of the interfaces they endpt, if any
	DevDelay(any, int) float64    // every device can be be queried for the delay it introduces for an operation
	DevState() any                // every device as a structure of state that can be accessed
	DevIsSimple() bool            // switches or routers can be 'simple'
	DevRng() *rngstream.RngStream // every device has its own RNG stream
	DevAddActive(*NetworkMsg)     // add the connectID argument to the device's list of active connections
	DevRmActive(int)              // remove the connectID argument to the device's list of active connections
	DevForward() DFS              // index by FlowID, yields map of ingress intrfc ID to egress intrfc ID
	LogNetEvent(vrtime.Time, *NetworkMsg, string)
}

// paramObj interface is satisfied by every network object that
// can be configured at run-time with performance parameters. These
// are intrfcStruct, networkStruct, switchDev, endptDev, routerDev
type paramObj interface {
	matchParam(string, string) bool
	setParam(string, valueStruct)
	paramObjName() string
	LogNetEvent(vrtime.Time, *NetworkMsg, string)
}

// The intrfcStruct holds information about a network interface embedded in a device
type intrfcStruct struct {
	Name     string          // unique name, probably generated automatically
	Groups   []string        // list of groups this interface may belong to
	Number   int             // unique integer id, probably generated automatically
	DevType  DevCode         // device code of the device holding the interface
	Media    NetworkMedia    // media of the network the interface interacts with
	Device   TopoDev         // pointer to the device holding the interface
	PrmDev   paramObj        // pointer to the device holding the interface as a paramObj
	Cable    *intrfcStruct   // For a wired interface, points to the "other" interface in the connection
	Carry    []*intrfcStruct // points to the "other" interface in a connection
	Wireless []*intrfcStruct // For a wired interface, points to the "other" interface in the connection
	Faces    *networkStruct  // pointer to the network the interface interacts with
	State    *intrfcState    // pointer to the interface's block of state information
}

// The  intrfcState holds parameters descriptive of the interface's capabilities
type intrfcState struct {
	Bndwdth        float64 // maximum bandwidth (in Mbytes/sec)
	BckgrndBW      float64 // deep background load consumes bandwidth
	BufferSize     float64 // buffer capacity (in Mbytes)
	Latency        float64 // time the leading bit takes to traverse the wire out of the interface
	Delay          float64 // time the leading bit takes to traverse the interface
	IngressTransit bool
	EgressTransit  bool
	Simple         bool // use the pass-through timing model
	MTU            int  // maximum packet size (bytes)
	Trace          bool // switch for calling add trace
	Drop           bool // whether to permit packet drops

	ToIngress   map[int]float64 // sum of flow rates into ingress side of device
	ThruIngress map[int]float64 // sum of flow rates out of ingress side of device
	ToEgress    map[int]float64 // sum of flow rates into egress side of device
	ThruEgress  map[int]float64 // sum of flow rates out of egress side device

	PcktClass     float64 // fraction of bandwidth reserved for packets
	IngressLambda float64 // sum of rates of flows approach interface from ingress side.
	EgressLambda  float64
	RsrvdFrac     float64 // fraction of bandwidth reserved for non-flow traffic
	end2endBW     float64 // scratch location
	priQueue      *ClassQueue
}

// useableBW returns the interface bandwidth after background load is removed
func (intrfc *intrfcStruct) useableBW() float64 {
	return intrfc.State.Bndwdth - intrfc.State.BckgrndBW
}

// createIntrfcState is a constructor, assumes defaults on unspecified attributes
func createIntrfcState() *intrfcState {
	iss := new(intrfcState)
	iss.Delay = 1e+6 // in seconds!  Set this way so that if not initialized we'll notice
	iss.Latency = 1e+6
	iss.MTU = 1500 // in bytes Set for Ethernet2 MTU, should change if wireless

	iss.ToIngress = make(map[int]float64)
	iss.ThruIngress = make(map[int]float64)
	iss.ToEgress = make(map[int]float64)
	iss.ThruEgress = make(map[int]float64)
	iss.EgressLambda = 0.0
	iss.IngressLambda = 0.0
	iss.RsrvdFrac = 0.0
	iss.priQueue = createClassQueue()
	return iss
}

// computeFlowWaits estimates the time a flow arrival in classID is in the system,
// for either an ingress or egress interface.  Called when an accepted flow rate changes
// Formula for class k waiting time (of flows) is
// k=1 is highest priority
// W_k : mean waiting time of class-k msgs
// S_k : mean service time of class-k msg
// lambda_k : arrival rate class k
// rho_k : load of class-k, rho_k = lambda_k*S_k
//
// R : mean residual of server on arrival : (server util)*D/2
//
//  W_k = \frac{R}/((1- \sum_{j=1}^{k-1} rho_j)*(1-\sum_{j=1}^{k} \rho_j))
//
// Note that S_k = D for all k, and that R = (D/2)*\sum_{j=1}^N \rho_j
//
// When a packet arrives we include the waiting time for all packets with equal or higher priority, so
// the waiting time for it is approximated by
//
// W = W_k + \sum{j=1}^k Q_j
//
// where Q_j is the number of packets in class j waiting for service.
//
//

// ComputeFlowWaits computes a model-based estimate of the waiting time
// due to higher priority flows
func (intrfc *intrfcStruct) ComputeFlowWaits(D float64, ingress bool) {
	CQ := intrfc.State.priQueue

	var Qs []*classQueue
	if ingress {
		Qs = CQ.ingressQs
	} else {
		Qs = CQ.egressQs
	}

	rho := make([]float64, len(Qs))
	rhoSum := make([]float64, len(Qs))
	var allRho float64

	for idx := 0; idx < len(Qs); idx++ {
		cg := Qs[idx]
		if ingress {
			rho[idx] = D * cg.ingressLambda
		} else {
			rho[idx] = D * cg.egressLambda
		}
		allRho += rho[idx]
		if idx == 0 {
			rhoSum[idx] = rho[idx]
		} else {
			rhoSum[idx] = rho[idx] + rhoSum[idx-1]
		}
	}
	rbar := allRho * D / 2.0

	for idx := 0; idx < len(Qs); idx++ {
		cg := Qs[idx]
		cg.waiting = rbar / ((1 - rhoSum[idx-1]) * (1 - rhoSum[idx]))
	}
}

// createIntrfcStruct is a constructor, building an intrfcStruct
// from a desc description of the interface
func createIntrfcStruct(intrfc *IntrfcDesc) *intrfcStruct {
	is := new(intrfcStruct)
	is.Groups = intrfc.Groups

	// name comes from desc description
	is.Name = intrfc.Name

	// unique id is locally generated
	is.Number = nxtID()

	// desc representation codes the device type as a string
	switch intrfc.DevType {
	case "Endpt":
		is.DevType = EndptCode
	case "Router":
		is.DevType = RouterCode
	case "Switch":
		is.DevType = SwitchCode
	}

	// The desc description gives the name of the device endpting the interface.
	// We can use this to look up the locally constructed representation of the device
	// and save a pointer to it
	is.Device = TopoDevByName[intrfc.Device]
	is.PrmDev = paramObjByName[intrfc.Device]

	// desc representation codes the media type as a string
	switch intrfc.MediaType {
	case "wired", "Wired":
		is.Media = Wired
	case "wireless", "Wireless":
		is.Media = Wireless
	default:
		is.Media = UnknownMedia
	}

	is.Wireless = make([]*intrfcStruct, 0)
	is.Carry = make([]*intrfcStruct, 0)

	is.State = createIntrfcState()

	return is
}

// non-preemptive priority
// k=1 is highest priority
// W_k : mean waiting time of class-k msgs
// S_k : mean service time of class-k msg
// lambda_k : arrival rate class k
// rho_k : load of class-k, rho_k = lambda_k*S_k
// R : mean residual of server on arrival : (server util)*D/2
//
//  W_k = R/((1-rho_{1}-rho_{2}- ... -rho_{k-1})*(1-rho_1-rho_2- ... -rho_k))
//
//  Mean time in system of class-k msg is T_k = W_k+S_k
//
// for our purposes we will use k=0 for least class, and use the formula
//  W_0 = R/((1-rho_{1}-rho_{2}- ... -rho_{k-1})*(1-rho_1-rho_2- ... -rho_{k-1}-rho_0))

// ShortIntrfc stores information we serialize for storage in a trace
type ShortIntrfc struct {
	DevName     string
	Faces       string
	ToIngress   float64
	ThruIngress float64
	ToEgress    float64
	ThruEgress  float64
	FlowID      int
	NetMsgType  NetworkMsgType
	Rate        float64
	PrArrvl     float64
	Time        float64
}

// Serialize turns a ShortIntrfc into a string, in yaml format
func (sis *ShortIntrfc) Serialize() string {
	var bytes []byte
	var merr error

	bytes, merr = yaml.Marshal(*sis)

	if merr != nil {
		panic(merr)
	}
	return string(bytes[:])
}

// addTrace gathers information about an interface and message
// passing though it, and prints it out
func (intrfc *intrfcStruct) addTrace(label string, nm *NetworkMsg, t float64) {
	if true || !intrfc.State.Trace {
		return
	}
	si := new(ShortIntrfc)
	si.DevName = intrfc.Device.DevName()
	si.Faces = intrfc.Faces.Name
	flwID := nm.FlowID
	si.FlowID = flwID
	si.ToIngress = intrfc.State.ToIngress[flwID]
	si.ThruIngress = intrfc.State.ThruIngress[flwID]
	si.ToEgress = intrfc.State.ToEgress[flwID]
	si.ThruEgress = intrfc.State.ThruEgress[flwID]

	si.NetMsgType = nm.NetMsgType
	si.PrArrvl = nm.PrArrvl
	si.Time = t
	siStr := si.Serialize()
	siStr = strings.Replace(siStr, "\n", " ", -1)
	fmt.Println(label, siStr)
}

// matchParam is used to determine whether a run-time parameter description
// should be applied to the interface. Its definition here helps intrfcStruct satisfy
// paramObj interface.  To apply or not to apply depends in part on whether the
// attribute given matchParam as input matches what the interface has. The
// interface attributes that can be tested are the device type of device that endpts it, and the
// media type of the network it interacts with
func (intrfc *intrfcStruct) matchParam(attrbName, attrbValue string) bool {
	switch attrbName {
	case "name":
		return intrfc.Name == attrbValue
	case "group":
		return slices.Contains(intrfc.Groups, attrbValue)
	case "media":
		return NetMediaFromStr(attrbValue) == intrfc.Media
	case "devtype":
		return DevCodeToStr(intrfc.Device.DevType()) == attrbValue
	case "devname":
		return intrfc.Device.DevName() == attrbValue
	}

	// an error really, as we should match only the names given in the switch statement above
	return false
}

// setParam assigns the parameter named in input with the value given in the input.
// N.B. the valueStruct has fields for integer, float, and string values.  Pick the appropriate one.
// setParam's definition here helps intrfcStruct satisfy the paramObj interface.
func (intrfc *intrfcStruct) setParam(paramType string, value valueStruct) {
	switch paramType {
	// latency, delay, and bandwidth are floats
	case "latency":
		// units of delay are seconds
		intrfc.State.Latency = value.floatValue
	case "delay":
		// units of delay are seconds
		intrfc.State.Delay = value.floatValue
	case "bandwidth":
		// units of bandwidth are Mbits/sec
		intrfc.State.Bndwdth = value.floatValue
	case "bckgrndBW":
		// units of bandwidth consumed by background traffic (not flows)
		intrfc.State.BckgrndBW = value.floatValue
	case "buffer":
		// units of buffer are Mbytes
		intrfc.State.BufferSize = value.floatValue
	case "MTU":
		// number of bytes in maximally sized packet
		intrfc.State.MTU = value.intValue
	case "trace":
		intrfc.State.Trace = value.boolValue
	case "drop":
		intrfc.State.Drop = value.boolValue
	case "rsrvd":
		intrfc.State.RsrvdFrac = value.floatValue
	}
}

// LogNetEvent creates and logs a network event from a message passing
// through this interface
func (intrfc *intrfcStruct) LogNetEvent(time vrtime.Time, nm *NetworkMsg, op string) {
	if !intrfc.State.Trace {
		return
	}
	AddNetTrace(devTraceMgr, time, nm, intrfc.Number, op)
}

// paramObjName helps intrfcStruct satisfy paramObj interface, returns interface name
func (intrfc *intrfcStruct) paramObjName() string {
	return intrfc.Name
}

// linkIntrfcStruct sets the 'connect' and 'faces' values
// of an intrfcStruct based on the names coded in a IntrfcDesc.
func linkIntrfcStruct(intrfcDesc *IntrfcDesc) {
	// look up the intrfcStruct corresponding to the interface named in input intrfc
	is := IntrfcByName[intrfcDesc.Name]

	// in IntrfcDesc the 'Cable' field is a string, holding the name of the target interface
	if len(intrfcDesc.Cable) > 0 {
		_, present := IntrfcByName[intrfcDesc.Cable]
		if !present {
			panic(fmt.Errorf("intrfc cable connection goof"))
		}
		is.Cable = IntrfcByName[intrfcDesc.Cable]
	}

	// in IntrfcDesc the 'Cable' field is a string, holding the name of the target interface
	if len(intrfcDesc.Carry) > 0 {
		for _, cintrfcName := range intrfcDesc.Carry {
			_, present := IntrfcByName[cintrfcName]
			if !present {
				panic(fmt.Errorf("intrfc cable connection goof"))
			}
			is.Carry = append(is.Carry, IntrfcByName[cintrfcName])
		}
	}

	if len(intrfcDesc.Wireless) > 0 {
		for _, IntrfcName := range intrfcDesc.Wireless {
			is.Wireless = append(is.Wireless, IntrfcByName[IntrfcName])
		}
	}

	// in IntrfcDesc the 'Faces' field is a string, holding the name of the network the interface
	// interacts with
	if len(intrfcDesc.Faces) > 0 {
		is.Faces = NetworkByName[intrfcDesc.Faces]
	}
}

// PcktDrop returns a flag indicating whether we're simulating packet drops
func (intrfc *intrfcStruct) PcktDrop() bool {
	return intrfc.State.Drop
}

// AddFlow initializes the To and Thru maps for the interface, creates a classQueue object
func (intrfc *intrfcStruct) AddFlow(flowID int, classID int, ingress bool) {
	if ingress {
		intrfc.State.ToIngress[flowID] = 0.0
		intrfc.State.ThruIngress[flowID] = 0.0
	} else {
		intrfc.State.ToEgress[flowID] = 0.0
		intrfc.State.ThruEgress[flowID] = 0.0
	}
	intrfc.State.priQueue.addClassID(classID, ingress)
}

// IsCongested determines whether the interface is congested,
// meaning that the bandwidth used by elastic flows is greater than or
// equal to the unreserved bandwidth
func (intrfc *intrfcStruct) IsCongested(ingress bool) bool {
	var usedBndwdth float64
	if ingress {
		for _, rate := range intrfc.State.ToIngress {
			usedBndwdth += rate
		}
	} else {
		for _, rate := range intrfc.State.ToEgress {
			usedBndwdth += rate
		}
	}

	// !( bandwidth for elastic flows < available bandwidth for elastic flows ) ==
	// !( usedBndwdth-fixedBndwdth < intrfc.State.Bndwdth-fixedBndwdth )
	// !( usedBndwdth < intrfc.State.Bndwdth )
	//
	useable := intrfc.useableBW()
	if usedBndwdth < useable && !(math.Abs(usedBndwdth-useable) < 1e-3) {
		return false
	}
	return true
}

// ChgFlowRate is called in the midst of changing the flow rates.
// The rate value 'rate' is one that flow has at this point in the
// computation, and the per-flow interface data structures are adjusted
// to reflect that
func (intrfc *intrfcStruct) ChgFlowRate(flowID int, classID int, rate float64, ingress bool) {
	cg := intrfc.State.priQueue.getClassQueue(classID, ingress)
	if ingress {
		oldRate := intrfc.State.ToIngress[flowID]
		intrfc.State.ToIngress[flowID] = rate
		intrfc.State.ThruIngress[flowID] = rate
		intrfc.State.IngressLambda += (rate - oldRate)
		cg.ingressLambda += (rate - oldRate)

	} else {
		oldRate := intrfc.State.ToEgress[flowID]
		intrfc.State.ToEgress[flowID] = rate
		intrfc.State.ThruEgress[flowID] = rate
		intrfc.State.EgressLambda += (rate - oldRate)
		cg.egressLambda += (rate - oldRate)
	}
}

// RmFlow adjusts data structures to reflect the removal of the identified flow, from the identified class,
// formerly having the identified rate
func (intrfc *intrfcStruct) RmFlow(flowID int, classID int, rate float64, ingress bool) {
	cg := intrfc.State.priQueue.getClassQueue(classID, ingress)
	if ingress {
		delete(intrfc.State.ToIngress, flowID)
		delete(intrfc.State.ThruIngress, flowID)

		// need have the removed flow's prior rate
		oldRate := cg.ingressLambda
		cg.ingressLambda = oldRate - rate
	} else {
		delete(intrfc.State.ToEgress, flowID)
		delete(intrfc.State.ThruEgress, flowID)

		// need have the removed flow's prior rate
		oldRate := cg.egressLambda
		cg.egressLambda = oldRate - rate
	}
}

// A networkStruct holds the attributes of one of the model's communication subnetworks
type networkStruct struct {
	Name        string        // unique name
	Groups      []string      // list of groups to which network belongs
	Number      int           // unique integer id
	NetScale    NetworkScale  // type, e.g., LAN, WAN, etc.
	NetMedia    NetworkMedia  // communication fabric, e.g., wired, wireless
	NetRouters  []*routerDev  // list of pointers to routerDevs with interfaces that face this subnetwork
	NetSwitches []*switchDev  // list of pointers to routerDevs with interfaces that face this subnetwork
	NetEndpts   []*endptDev   // list of pointers to routerDevs with interfaces that face this subnetwork
	NetState    *networkState // pointer to a block of information comprising the network 'state'
}

// A networkState struct holds some static and dynamic information about the network's current state
type networkState struct {
	Latency  float64 // latency through network (without considering explicitly declared wired connections) under no load
	Bndwdth  float64 //
	Capacity float64 // maximum traffic capacity of network
	Trace    bool    // switch for calling trace saving
	Drop     bool    // switch for dropping packets with random sampling
	Rngstrm  *rngstream.RngStream

	ClassID      map[int]int     // map of flow ID to reservation ID
	ClassBndwdth map[int]float64 // map of reservation ID to reserved bandwidth

	PcktClass float64
	BckgrndBW float64
	// revisit
	Flows   map[int]ActiveRec
	Forward map[int]map[intrfcIDPair]float64

	Load    float64 // real-time value of total load (in units of Mbytes/sec)
	Packets int     // number of packets actively passing in network
}

// AddFlow updates a networkStruct's data structures to add
// a major flow
func (ns *networkStruct) AddFlow(flowID int, classID int, ifcpr intrfcIDPair) {
	_, present := ns.NetState.Flows[flowID]
	if !present {
		ns.NetState.Flows[flowID] = ActiveRec{Number: 0, Rate: 0.0}
	}
	ar := ns.NetState.Flows[flowID]
	ar.Number += 1
	ns.NetState.Flows[flowID] = ar
	ns.NetState.Forward[flowID] = make(map[intrfcIDPair]float64)
	ns.NetState.Forward[flowID][ifcpr] = 0.0
}

// RmFlow updates a networkStruct's data structures to reflect
// removal of a major flow
func (ns *networkStruct) RmFlow(flowID int, ifcpr intrfcIDPair) {
	rate := ns.NetState.Forward[flowID][ifcpr]
	delete(ns.NetState.Forward[flowID], ifcpr)

	ar := ns.NetState.Flows[flowID]
	ar.Number -= 1
	ar.Rate -= rate
	if ar.Number == 0 {
		delete(ns.NetState.Flows, flowID)
		delete(ns.NetState.Forward, flowID)
	} else {
		ns.NetState.Flows[flowID] = ar
	}
}

// ChgFlowRate updates a networkStruct's data structures to reflect
// a change in the requested flow rate for the named flow
func (ns *networkStruct) ChgFlowRate(flowID int, ifcpr intrfcIDPair, rate float64) {

	// initialize (if needed) the forward entry for this flow
	oldRate, present := ns.NetState.Forward[flowID][ifcpr]
	if !present {
		ns.NetState.Forward[flowID] = make(map[intrfcIDPair]float64)
		oldRate = 0.0
	}

	// compute the change of rate and add the change to the variables
	// that accumulate the rates
	deltaRate := rate - oldRate

	ar := ns.NetState.Flows[flowID]
	ar.Rate += deltaRate
	ns.NetState.Flows[flowID] = ar

	// save the new rate
	ns.NetState.Forward[flowID][ifcpr] = rate
}

// determine whether the network state between the source and destination interfaces is congested,
// which can only happen if the interface bandwidth is larger than the configured bandwidth
// between these endpoints, and is busy enough to overwhelm it
func (ns *networkStruct) IsCongested(srcIntrfc, dstIntrfc *intrfcStruct) bool {

	// gather the fixed bandwdth for the source
	usedBndwdth := math.Min(srcIntrfc.State.RsrvdFrac*srcIntrfc.State.Bndwdth,
		dstIntrfc.State.RsrvdFrac*dstIntrfc.State.Bndwdth)

	seenFlows := make(map[int]bool)
	for flwID, rate := range srcIntrfc.State.ThruEgress {
		usedBndwdth += rate
		seenFlows[flwID] = true
	}

	for flwID, rate := range dstIntrfc.State.ToIngress {
		if seenFlows[flwID] {
			continue
		}
		usedBndwdth += rate
	}
	net := srcIntrfc.Faces
	netuseableBW := net.useableBW()
	if usedBndwdth < netuseableBW && !(math.Abs(usedBndwdth-netuseableBW) < 1e-3) {
		return false
	}
	return true
}

// initNetworkStruct transforms information from the desc description
// of a network to its networkStruct representation.  This is separated from
// the createNetworkStruct constructor because it requires that the brdcstDmnByName
// and RouterDevByName lists have been created, which in turn requires that
// the router constructors have already been called.  So the call to initNetworkStruct
// is delayed until all of the network device constructors have been called.
func (ns *networkStruct) initNetworkStruct(nd *NetworkDesc) {
	// in NetworkDesc a router is referred to through its string name
	ns.NetRouters = make([]*routerDev, 0)
	for _, rtrName := range nd.Routers {
		// use the router name from the desc representation to find the run-time pointer
		// to the router and append to the network's list
		ns.AddRouter(RouterDevByName[rtrName])
	}

	ns.NetEndpts = make([]*endptDev, 0)
	for _, EndptName := range nd.Endpts {
		// use the router name from the desc representation to find the run-time pointer
		// to the router and append to the network's list
		ns.addEndpt(EndptDevByName[EndptName])
	}

	ns.NetSwitches = make([]*switchDev, 0)
	for _, SwitchName := range nd.Switches {
		// use the router name from the desc representation to find the run-time pointer
		// to the router and append to the network's list
		ns.AddSwitch(SwitchDevByName[SwitchName])
	}
	ns.Groups = nd.Groups
}

// createNetworkStruct is a constructor that initialized some of the features of the networkStruct
// from their expression in a desc representation of the network
func createNetworkStruct(nd *NetworkDesc) *networkStruct {
	ns := new(networkStruct)

	// copy the name
	ns.Name = nd.Name

	ns.Groups = []string{}

	// get a unique integer id locally
	ns.Number = nxtID()

	// get a netScale type from a desc string expression of it
	ns.NetScale = NetScaleFromStr(nd.NetScale)

	// get a netMedia type from a desc string expression of it
	ns.NetMedia = NetMediaFromStr(nd.MediaType)

	// initialize the Router lists, to be filled in by
	// initNetworkStruct after the router constructors are called
	ns.NetRouters = make([]*routerDev, 0)
	ns.NetEndpts = make([]*endptDev, 0)

	// make the state structure, will flesh it out from run-time configuration parameters
	ns.NetState = createNetworkState(ns.Name)
	return ns
}

func (ns *networkStruct) useableBW() float64 {
	return ns.NetState.Bndwdth - ns.NetState.BckgrndBW
}

// createNetworkState constructs the data for a networkState struct
func createNetworkState(name string) *networkState {
	ns := new(networkState)
	ns.Flows = make(map[int]ActiveRec)
	ns.Forward = make(map[int]map[intrfcIDPair]float64)
	ns.ClassID = make(map[int]int)
	ns.ClassBndwdth = make(map[int]float64)
	ns.Packets = 0
	ns.Drop = false
	ns.Rngstrm = rngstream.New(name)
	ns.PcktClass = 0.05
	return ns
}

// NetLatency estimates the time required by a message to traverse the network,
// using the mean time in system of an M/D/1 queue
func (ns *networkStruct) NetLatency(nm *NetworkMsg) float64 {
	// get the rate activity on this channel
	lambda := ns.channelLoad(nm)

	// compute the service mean
	srv := (float64(nm.MsgLen*8) / 1e6) / ns.NetState.Bndwdth

	rho := lambda * srv
	inSys := srv + rho/(2*(1.0/srv)*(1-rho))
	return ns.NetState.Latency + inSys
}

// matchParam is used to determine whether a run-time parameter description
// should be applied to the network. Its definition here helps networkStruct satisfy
// paramObj interface.  To apply or not to apply depends in part on whether the
// attribute given matchParam as input matches what the interface has. The
// interface attributes that can be tested are the media type, and the nework type
func (ns *networkStruct) matchParam(attrbName, attrbValue string) bool {
	switch attrbName {
	case "name":
		return ns.Name == attrbValue
	case "group":
		return slices.Contains(ns.Groups, attrbValue)
	case "media":
		return NetMediaFromStr(attrbValue) == ns.NetMedia
	case "scale":
		return ns.NetScale == NetScaleFromStr(attrbValue)
	}

	// an error really, as we should match only the names given in the switch statement above
	return false
}

// setParam assigns the parameter named in input with the value given in the input.
// N.B. the valueStruct has fields for integer, float, and string values.  Pick the appropriate one.
// setParam's definition here helps networkStruct satisfy the paramObj interface.
func (ns *networkStruct) setParam(paramType string, value valueStruct) {
	// for some attributes we'll want the string-based value, for others the floating point one
	fltValue := value.floatValue

	// branch on the parameter being set
	switch paramType {
	case "latency":
		ns.NetState.Latency = fltValue
	case "bandwidth":
		ns.NetState.Bndwdth = fltValue
	case "bckgrndBW":
		ns.NetState.BckgrndBW = fltValue
	case "capacity":
		ns.NetState.Capacity = fltValue
	case "trace":
		ns.NetState.Trace = value.boolValue
	case "drop":
		ns.NetState.Drop = value.boolValue
	}
}

// paramObjName helps networkStruct satisfy paramObj interface, returns network name
func (ns *networkStruct) paramObjName() string {
	return ns.Name
}

// LogNetEvent saves a log event associated with the network
func (ns *networkStruct) LogNetEvent(time vrtime.Time, nm *NetworkMsg, op string) {
	if !ns.NetState.Trace {
		return
	}
	AddNetTrace(devTraceMgr, time, nm, ns.Number, op)
}

// AddRouter includes the router given as input parameter on the network list of routers that face it
func (ns *networkStruct) AddRouter(newrtr *routerDev) {
	// skip if rtr already exists in network netRouters list
	for _, rtr := range ns.NetRouters {
		if rtr == newrtr || rtr.RouterName == newrtr.RouterName {
			return
		}
	}
	ns.NetRouters = append(ns.NetRouters, newrtr)
}

// addEndpt includes the endpt given as input parameter on the network list of endpts that face it
func (ns *networkStruct) addEndpt(newendpt *endptDev) {
	// skip if endpt already exists in network netEndpts list
	for _, endpt := range ns.NetEndpts {
		if endpt == newendpt || endpt.EndptName == newendpt.EndptName {
			return
		}
	}
	ns.NetEndpts = append(ns.NetEndpts, newendpt)
}

// AddSwitch includes the swtch given as input parameter on the network list of swtchs that face it
func (ns *networkStruct) AddSwitch(newswtch *switchDev) {
	// skip if swtch already exists in network.NetSwitches list
	for _, swtch := range ns.NetSwitches {
		if swtch == newswtch || swtch.SwitchName == newswtch.SwitchName {
			return
		}
	}
	ns.NetSwitches = append(ns.NetSwitches, newswtch)
}

// ServiceRate identifies the rate of the network when viewed as a separated server,
// which means the bndwdth of the channel (possibly implicit) excluding known flows)
func (ns *networkStruct) channelLoad(nm *NetworkMsg) float64 {
	rtStep := (*nm.Route)[nm.StepIdx]
	srcIntrfc := IntrfcByID[rtStep.srcIntrfcID]
	dstIntrfc := IntrfcByID[rtStep.dstIntrfcID]
	return srcIntrfc.State.EgressLambda + dstIntrfc.State.IngressLambda
}

// PcktDrop returns the packet drop bit for the network
func (ns *networkStruct) PcktDrop() bool {
	return ns.NetState.Drop
}

// a endptDev holds information about a endpt
type endptDev struct {
	EndptName       string   // unique name
	EndptGroups     []string // list of groups to which endpt belongs
	EndptModel      string   // model of CPU the endpt uses
	EndptCores      int
	EndptSched      *TaskScheduler            // shares an endpoint's cores among computing tasks
	EndptAccelSched map[string]*TaskScheduler // map of accelerators, indexed by type name
	EndptAccelModel map[string]string         // accel device name on endpoint mapped to device model for timing
	EndptID         int                       // unique integer id
	EndptIntrfcs    []*intrfcStruct           // list of network interfaces embedded in the endpt
	EndptState      *endptState               // a struct holding endpt state
}

// a endptState holds extra informat used by the endpt
type endptState struct {
	Rngstrm *rngstream.RngStream // pointer to a random number generator
	Trace   bool                 // switch for calling add trace
	Drop    bool                 // whether to support packet drops at interface
	Active  map[int]float64
	Load    float64
	Forward DFS
	Packets int

	BckgrndRate float64
	BckgrndSrv  float64
	BckgrndIdx  int
}

// matchParam is for other paramObj objects a method for seeing whether
// the device attribute matches the input.  'cept the endptDev is not declared
// to have any such attributes, so this function (included to let endptDev be
// a paramObj) returns false.  Included to allow endptDev to satisfy paramObj interface requirements
func (endpt *endptDev) matchParam(attrbName, attrbValue string) bool {
	switch attrbName {
	case "name":
		return endpt.EndptName == attrbValue
	case "group":
		return slices.Contains(endpt.EndptGroups, attrbValue)
	case "model":
		return endpt.EndptModel == attrbValue
	}

	// an error really, as we should match only the names given in the switch statement above
	return false
}

// setParam gives a value to a endptDev parameter.  The design allows only
// the CPU model parameter to be set, which is allowed here
func (endpt *endptDev) setParam(param string, value valueStruct) {
	switch param {
	case "trace":
		endpt.EndptState.Trace = value.boolValue
	case "model":
		endpt.EndptModel = value.stringValue
	case "cores":
		endpt.EndptCores = value.intValue
	case "bckgrndRate":
		endpt.EndptState.BckgrndRate = value.floatValue
	case "bckgrndSrv":
		endpt.EndptState.BckgrndSrv = value.floatValue
	}
}

// paramObjName helps endptDev satisfy paramObj interface, returns the endpt's name
func (endpt *endptDev) paramObjName() string {
	return endpt.EndptName
}

// createEndptDev is a constructor, using information from the desc description of the endpt
func createEndptDev(endptDesc *EndptDesc) *endptDev {
	endpt := new(endptDev)
	endpt.EndptName = endptDesc.Name // unique name
	endpt.EndptModel = endptDesc.Model
	endpt.EndptCores = endptDesc.Cores
	endpt.EndptID = nxtID() // unique integer id, generated at model load-time
	endpt.EndptState = createEndptState(endpt.EndptName)
	endpt.EndptIntrfcs = make([]*intrfcStruct, 0) // initialization of list of interfaces, to be augmented later

	endpt.EndptGroups = make([]string, len(endptDesc.Groups))
	copy(endpt.EndptGroups, endptDesc.Groups)

	endpt.EndptAccelSched = make(map[string]*TaskScheduler)
	endpt.EndptAccelModel = make(map[string]string)

	var accelName string
	accelCores := 1
	for accelCode, accelModel := range endptDesc.Accel {
		// if there is a "," split out the accelerator name and number of cores
		if strings.Contains(accelCode, ",") {
			pieces := strings.Split(accelCode, ",")
			accelName = pieces[0]
			accelCores, _ = strconv.Atoi(pieces[1])
		} else {
			accelName = accelCode
		}
		endpt.EndptAccelModel[accelName] = accelModel
		endpt.EndptAccelSched[accelName] = CreateTaskScheduler(accelCores)
	}
	AccelSchedulersByHostName[endpt.EndptName] = endpt.EndptAccelSched
	return endpt
}

// createEndptState constructs the data for the endpoint state
func createEndptState(name string) *endptState {
	eps := new(endptState)
	eps.Active = make(map[int]float64)
	eps.Load = 0.0
	eps.Packets = 0
	eps.Trace = false
	eps.Forward = make(DFS)
	eps.Rngstrm = rngstream.New(name)

	// default, nothing happens
	eps.BckgrndRate = 0.0
	eps.BckgrndSrv = 0.0
	return eps
}

// initTaskScheduler calls CreateTaskScheduler to create
// the logic for incorporating the impacts of parallel cores
// on execution time
func (endpt *endptDev) initTaskScheduler() {
	scheduler := CreateTaskScheduler(endpt.EndptCores)
	endpt.EndptSched = scheduler
	// remove if already present
	delete(TaskSchedulerByHostName, endpt.EndptName)
	TaskSchedulerByHostName[endpt.EndptName] = scheduler
}

// addIntrfc appends the input intrfcStruct to the list of interfaces embedded in the endpt.
func (endpt *endptDev) addIntrfc(intrfc *intrfcStruct) {
	endpt.EndptIntrfcs = append(endpt.EndptIntrfcs, intrfc)
}

// CPUModel returns the string type description of the CPU model running the endpt
func (endpt *endptDev) CPUModel() string {
	return endpt.EndptModel
}

// DevName returns the endpt name, as part of the TopoDev interface
func (endpt *endptDev) DevName() string {
	return endpt.EndptName
}

// DevID returns the endpt integer id, as part of the TopoDev interface
func (endpt *endptDev) DevID() int {
	return endpt.EndptID
}

// DevType returns the endpt's device type, as part of the TopoDev interface
func (endpt *endptDev) DevType() DevCode {
	return EndptCode
}

// DevIntrfcs returns the endpt's list of interfaces, as part of the TopoDev interface
func (endpt *endptDev) DevIntrfcs() []*intrfcStruct {
	return endpt.EndptIntrfcs
}

// DevState returns the endpt's state struct, as part of the TopoDev interface
func (endpt *endptDev) DevState() any {
	return endpt.EndptState
}

func (endpt *endptDev) DevForward() DFS {
	return endpt.EndptState.Forward
}

// devRng returns the endpt's rng pointer, as part of the TopoDev interface
func (endpt *endptDev) DevRng() *rngstream.RngStream {
	return endpt.EndptState.Rngstrm
}

func (endpt *endptDev) LogNetEvent(time vrtime.Time, nm *NetworkMsg, op string) {
	if !endpt.EndptState.Trace {
		return
	}
	AddNetTrace(devTraceMgr, time, nm, endpt.EndptID, op)
}

// DevAddActive adds an active connection, as part of the TopoDev interface.  Not used for endpts, yet
func (endpt *endptDev) DevAddActive(nme *NetworkMsg) {
	endpt.EndptState.Active[nme.ConnectID] = nme.Rate
}

// DevRmActive removes an active connection, as part of the TopoDev interface.  Not used for endpts, yet
func (endpt *endptDev) DevRmActive(connectID int) {
	delete(endpt.EndptState.Active, connectID)
}

// DevDelay returns the state-dependent delay for passage through the device, as part of the TopoDev interface.
// Not really applicable to endpt, so zero is returned
func (endpt *endptDev) DevDelay(arg any, msgLen int) float64 {
	return 0.0
}

// DevIsSimple states that the endpoint is not simple
func (endpt *endptDev) DevIsSimple() bool {
	return false
}

// The switchDev struct holds information describing a run-time representation of a switch
type switchDev struct {
	SwitchName    string          // unique name
	SwitchGroups  []string        // groups to which the switch may belong
	SwitchModel   string          // model name, used to identify performance characteristics
	SwitchID      int             // unique integer id, generated at model-load time
	SwitchIntrfcs []*intrfcStruct // list of network interfaces embedded in the switch
	SwitchState   *switchState    // pointer to the switch's state struct
}

// The switchState struct holds auxiliary information about the switch
type switchState struct {
	Rngstrm    *rngstream.RngStream // pointer to a random number generator
	Trace      bool                 // switch for calling trace saving
	Drop       bool                 // switch to allow dropping packets
	Simple     bool                 // simple switches use a constant to transfer through
	Active     map[int]float64
	Load       float64
	BufferSize float64
	Capacity   float64
	Forward    DFS
	Packets    int
}

// createSwitchDev is a constructor, initializing a run-time representation of a switch from its desc description
func createSwitchDev(switchDesc *SwitchDesc) *switchDev {
	swtch := new(switchDev)
	swtch.SwitchName = switchDesc.Name
	swtch.SwitchModel = switchDesc.Model
	swtch.SwitchID = nxtID()
	swtch.SwitchIntrfcs = make([]*intrfcStruct, 0)
	swtch.SwitchGroups = switchDesc.Groups
	swtch.SwitchState = createSwitchState(swtch.SwitchName)

	if switchDesc.Simple == 1 {
		swtch.SwitchState.Simple = true
	} else {
		swtch.SwitchState.Simple = false
	}
	return swtch
}

// createSwitchState constructs data structures for the switch's state
func createSwitchState(name string) *switchState {
	ss := new(switchState)
	ss.Active = make(map[int]float64)
	ss.Load = 0.0
	ss.Packets = 0
	ss.Trace = false
	ss.Drop = false
	ss.Rngstrm = rngstream.New(name)
	ss.Forward = make(map[int]intrfcIDPair)
	return ss
}

// DevForward returns the switch's forward table
func (swtch *switchDev) DevForward() DFS {
	return swtch.SwitchState.Forward
}

// addForward adds an ingress/egress pair to the switch's forwarding table for a flowID
func (swtch *switchDev) addForward(flowID int, idp intrfcIDPair) {
	swtch.SwitchState.Forward[flowID] = idp
}

// rmForward removes a flowID from the switch's forwarding table
func (swtch *switchDev) rmForward(flowID int) {
	delete(swtch.SwitchState.Forward, flowID)
}

// matchParam is used to determine whether a run-time parameter description
// should be applied to the switch. Its definition here helps switchDev satisfy
// the paramObj interface.  To apply or not to apply depends in part on whether the
// attribute given matchParam as input matches what the switch has. 'model' is the
// only attribute we use to match a switch
func (swtch *switchDev) matchParam(attrbName, attrbValue string) bool {
	switch attrbName {
	case "name":
		return swtch.SwitchName == attrbValue
	case "group":
		return slices.Contains(swtch.SwitchGroups, attrbValue)
	case "model":
		return swtch.SwitchModel == attrbValue
	}

	// an error really, as we should match only the names given in the switch statement above
	return false
}

// setParam gives a value to a switchDev parameter, to help satisfy the paramObj interface.
// Parameters that can be altered on a switch are "model", "execTime", and "buffer"
func (swtch *switchDev) setParam(param string, value valueStruct) {
	switch param {
	case "model":
		swtch.SwitchModel = value.stringValue
	case "buffer":
		swtch.SwitchState.BufferSize = value.floatValue
	case "trace":
		swtch.SwitchState.Trace = value.boolValue
	case "drop":
		swtch.SwitchState.Drop = value.boolValue
	case "simple":
		swtch.SwitchState.Simple = value.boolValue
	}
}

// paramObjName returns the switch name, to help satisfy the paramObj interface.
func (swtch *switchDev) paramObjName() string {
	return swtch.SwitchName
}

// addIntrfc appends the input intrfcStruct to the list of interfaces embedded in the switch.
func (swtch *switchDev) addIntrfc(intrfc *intrfcStruct) {
	swtch.SwitchIntrfcs = append(swtch.SwitchIntrfcs, intrfc)
}

// DevName returns the switch name, as part of the TopoDev interface
func (swtch *switchDev) DevName() string {
	return swtch.SwitchName
}

// DevID returns the switch integer id, as part of the TopoDev interface
func (swtch *switchDev) DevID() int {
	return swtch.SwitchID
}

// DevType returns the switch's device type, as part of the TopoDev interface
func (swtch *switchDev) DevType() DevCode {
	return SwitchCode
}

// DevIntrfcs returns the switch's list of interfaces, as part of the TopoDev interface
func (swtch *switchDev) DevIntrfcs() []*intrfcStruct {
	return swtch.SwitchIntrfcs
}

// DevState returns the switch's state struct, as part of the TopoDev interface
func (swtch *switchDev) DevState() any {
	return swtch.SwitchState
}

// DevRng returns the switch's rng pointer, as part of the TopoDev interface
func (swtch *switchDev) DevRng() *rngstream.RngStream {
	return swtch.SwitchState.Rngstrm
}

// DevAddActive adds a connection id to the list of active connections through the switch, as part of the TopoDev interface
func (swtch *switchDev) DevAddActive(nme *NetworkMsg) {
	swtch.SwitchState.Active[nme.ConnectID] = nme.Rate
}

// devRmActive removes a connection id to the list of active connections through the switch, as part of the TopoDev interface
func (swtch *switchDev) DevRmActive(connectID int) {
	delete(swtch.SwitchState.Active, connectID)
}

// devDelay returns the state-dependent delay for passage through the switch, as part of the TopoDev interface.
func (swtch *switchDev) DevDelay(msg any, msgLen int) float64 {
	return passThruDelay("switch", swtch.SwitchModel, msgLen)
}

func (swtch *switchDev) DevIsSimple() bool {
	return swtch.SwitchState.Simple
}

// LogNetEvent satisfies TopoDev interface
func (swtch *switchDev) LogNetEvent(time vrtime.Time, nm *NetworkMsg, op string) {
	if !swtch.SwitchState.Trace {
		return
	}
	AddNetTrace(devTraceMgr, time, nm, swtch.SwitchID, op)
}

// The routerDev struct holds information describing a run-time representation of a router
type routerDev struct {
	RouterName    string          // unique name
	RouterGroups  []string        // list of groups to which the router belongs
	RouterModel   string          // attribute used to identify router performance characteristics
	RouterID      int             // unique integer id assigned at model-load time
	RouterIntrfcs []*intrfcStruct // list of interfaces embedded in the router
	RouterState   *routerState    // pointer to the struct of the routers auxiliary state
}

// The routerState type describes auxiliary information about the router
type routerState struct {
	Rngstrm *rngstream.RngStream // pointer to a random number generator
	Trace   bool                 // switch for calling trace saving
	Drop    bool                 // switch to allow dropping packets
	Simple  bool                 // use a simple constant for transfer
	Active  map[int]float64
	Load    float64
	Buffer  float64
	Forward map[int]intrfcIDPair
	Packets int
}

// createRouterDev is a constructor, initializing a run-time representation of a router from its desc representation
func createRouterDev(routerDesc *RouterDesc) *routerDev {
	router := new(routerDev)
	router.RouterName = routerDesc.Name
	router.RouterModel = routerDesc.Model
	router.RouterID = nxtID()
	router.RouterIntrfcs = make([]*intrfcStruct, 0)
	router.RouterGroups = routerDesc.Groups
	router.RouterState = createRouterState(router.RouterName)

	if routerDesc.Simple == 1 {
		router.RouterState.Simple = true
	} else {
		router.RouterState.Simple = false
	}
	return router
}

// createRouterState is a constructor, initializing the State dictionary for a router
func createRouterState(name string) *routerState {
	rs := new(routerState)
	rs.Active = make(map[int]float64)
	rs.Load = 0.0
	rs.Buffer = math.MaxFloat64 / 2.0
	rs.Packets = 0
	rs.Trace = false
	rs.Drop = false
	rs.Rngstrm = rngstream.New(name)
	rs.Forward = make(map[int]intrfcIDPair)
	return rs
}

// DevForward returns the router's forwarding table
func (router *routerDev) DevForward() DFS {
	return router.RouterState.Forward
}

// addForward adds an flowID entry to the router's forwarding table
func (router *routerDev) addForward(flowID int, idp intrfcIDPair) {
	router.RouterState.Forward[flowID] = idp
}

// rmForward removes a flowID from the router's forwarding table
func (router *routerDev) rmForward(flowID int) {
	delete(router.RouterState.Forward, flowID)
}

// matchParam is used to determine whether a run-time parameter description
// should be applied to the router. Its definition here helps switchDev satisfy
// the paramObj interface.  To apply or not to apply depends in part on whether the
// attribute given matchParam as input matches what the router has. 'model' is the
// only attribute we use to match a router
func (router *routerDev) matchParam(attrbName, attrbValue string) bool {
	switch attrbName {
	case "name":
		return router.RouterName == attrbValue
	case "group":
		return slices.Contains(router.RouterGroups, attrbValue)
	case "model":
		return router.RouterModel == attrbValue
	}

	// an error really, as we should match only the names given in the switch statement above
	return false
}

// setParam gives a value to a routerDev parameter, to help satisfy the paramObj interface.
// Parameters that can be altered on a router are "model", "execTime", and "buffer"
func (router *routerDev) setParam(param string, value valueStruct) {
	switch param {
	case "model":
		router.RouterModel = value.stringValue
	case "buffer":
		router.RouterState.Buffer = value.floatValue
	case "trace":
		router.RouterState.Trace = value.boolValue
	case "drop":
		router.RouterState.Drop = value.boolValue
	case "simple":
		router.RouterState.Simple = value.boolValue
	}
}

// paramObjName returns the router name, to help satisfy the paramObj interface.
func (router *routerDev) paramObjName() string {
	return router.RouterName
}

// addIntrfc appends the input intrfcStruct to the list of interfaces embedded in the router.
func (router *routerDev) addIntrfc(intrfc *intrfcStruct) {
	router.RouterIntrfcs = append(router.RouterIntrfcs, intrfc)
}

// DevName returns the router name, as part of the TopoDev interface
func (router *routerDev) DevName() string {
	return router.RouterName
}

// DevID returns the switch integer id, as part of the TopoDev interface
func (router *routerDev) DevID() int {
	return router.RouterID
}

// DevType returns the router's device type, as part of the TopoDev interface
func (router *routerDev) DevType() DevCode {
	return RouterCode
}

// DevIntrfcs returns the routers's list of interfaces, as part of the TopoDev interface
func (router *routerDev) DevIntrfcs() []*intrfcStruct {
	return router.RouterIntrfcs
}

// DevState returns the routers's state struct, as part of the TopoDev interface
func (router *routerDev) DevState() any {
	return router.RouterState
}

// DevRng returns a pointer to the routers's rng struct, as part of the TopoDev interface
func (router *routerDev) DevRng() *rngstream.RngStream {
	return router.RouterState.Rngstrm
}

// LogNetEvent includes a network trace report
func (router *routerDev) LogNetEvent(time vrtime.Time, nm *NetworkMsg, op string) {
	if !router.RouterState.Trace {
		return
	}
	AddNetTrace(devTraceMgr, time, nm, router.RouterID, op)
}

// DevAddActive includes a connectID as part of what is active at the device, as part of the TopoDev interface
func (router *routerDev) DevAddActive(nme *NetworkMsg) {
	router.RouterState.Active[nme.ConnectID] = nme.Rate
}

// DevRmActive removes a connectID as part of what is active at the device, as part of the TopoDev interface
func (router *routerDev) DevRmActive(connectID int) {
	delete(router.RouterState.Active, connectID)
}

// DevDelay returns the state-dependent delay for passage through the router, as part of the TopoDev interface.
func (router *routerDev) DevDelay(msg any, msgLen int) float64 {
	return passThruDelay("route", router.RouterModel, msgLen)
}

// DevIsSimple returns the bit indicating whether the router is simple
func (router *routerDev) DevIsSimple() bool {
	return router.RouterState.Simple
}

// The intrfcsToDev struct describes a connection to a device.  Used in route descriptions
type intrfcsToDev struct {
	srcIntrfcID int // id of the interface where the connection starts
	dstIntrfcID int // id of the interface embedded by the target device
	netID       int // id of the network between the src and dst interfaces
	devID       int // id of the device the connection targets
}

// The NetworkMsg type creates a wrapper for a message between comp pattern funcs.
// One value (StepIdx) indexes into a list of route steps, so that by incrementing
// we can find 'the next' step in the route.  One value is a pointer to this route list,
// and the final value is a pointe to an inter-func comp pattern message.
type NetworkMsg struct {
	StepIdx    int             // position within the route from source to destination
	Route      *[]intrfcsToDev // pointer to description of route
	Connection ConnDesc        // {DiscreteConn, MajorFlowConn, MinorFlowConn}
	ExecID     int
	FlowID     int            // flow id given by app at entry
	ClassID    int            // if > 0 means the message uses that reservation
	ConnectID  int            // connection identifier
	NetMsgType NetworkMsgType // enum type packet,
	PcktRate   float64        // if ClassID>0, the class arrival rate. Otherwise from source
	Rate       float64        // flow rate if FlowID >0 (meaning a flow)
	StartTime  float64        // simuation time when the message entered the network
	PrArrvl    float64        // probablity of arrival
	MsgLen     int            // length of the entire message, in Mbytes
	PcktIdx    int            // index of packet with msg
	NumPckts   int            // number of packets in the message this is part of
	Msg        any            // message being carried.
}

// carriesPckt returns a boolean indicating whether the message is a packet (verses flow)
func (nm *NetworkMsg) carriesPckt() bool {
	return nm.NetMsgType == PacketType
}

// Pt2ptLatency computes the latency on a point-to-point connection
// between interfaces.  Called when neither interface is attached to a router
func Pt2ptLatency(srcIntrfc, dstIntrfc *intrfcStruct) float64 {
	return math.Max(srcIntrfc.State.Latency, dstIntrfc.State.Latency)
}

// currentIntrfcs returns pointers to the source and destination interfaces whose
// id values are carried in the current step of the route followed by the input argument NetworkMsg.
// If the interfaces are not directly connected but communicate through a network,
// a pointer to that network is returned also
func currentIntrfcs(nm *NetworkMsg) (*intrfcStruct, *intrfcStruct, *networkStruct) {
	srcIntrfcID := (*nm.Route)[nm.StepIdx].srcIntrfcID
	dstIntrfcID := (*nm.Route)[nm.StepIdx].dstIntrfcID

	srcIntrfc := IntrfcByID[srcIntrfcID]
	dstIntrfc := IntrfcByID[dstIntrfcID]

	var ns *networkStruct = nil
	netID := (*nm.Route)[nm.StepIdx].netID
	if netID != -1 {
		ns = NetworkByID[netID]
	} else {
		ns = NetworkByID[commonNetID(srcIntrfc, dstIntrfc)]
	}

	return srcIntrfc, dstIntrfc, ns
}

// transitDelay returns the length of time (in seconds) taken
// by the input argument NetworkMsg to traverse the current step in the route it follows.
// That step may be a point-to-point wired connection, or may be transition through a network
// w/o specification of passage through specific devices. In addition a pointer to the
// network transited (if any) is returned
func transitDelay(nm *NetworkMsg) (float64, *networkStruct) {
	var delay float64

	// recover the interfaces themselves and the network between them, if any
	srcIntrfc, dstIntrfc, net := currentIntrfcs(nm)

	if (srcIntrfc.Cable != nil && dstIntrfc.Cable == nil) ||
		(srcIntrfc.Cable == nil && dstIntrfc.Cable != nil) {
		panic("cabled interface confusion")
	}

	if srcIntrfc.Cable == nil {
		// delay is through network (baseline)
		delay = net.NetLatency(nm)
	} else {
		// delay is across a pt-to-pt line
		delay = Pt2ptLatency(srcIntrfc, dstIntrfc)
	}
	return delay, net
}

// dstBndwdth returns the receiving bandwidth of the destination interface, considering
// slow-down due to congestion
func dstBndwdth(nm *NetworkMsg) float64 {

	// recover the interfaces themselves and the network between them, if any
	srcIntrfc, dstIntrfc, _ := currentIntrfcs(nm)

	if srcIntrfc.Cable != nil {
		return dstIntrfc.useableBW()
	}
	return dstIntrfc.useableBW() - dstIntrfc.State.IngressLambda
}

// enterEgressIntrfc handles the arrival of a packet to the egress interface side of a device.
// It will have been forwarded from the device if an endpoint, or from the ingress interface
func enterEgressIntrfc(evtMgr *evtm.EventManager, egressIntrfc any, msg any) any {
	// cast context argument to interface
	intrfc := egressIntrfc.(*intrfcStruct)

	// cast data argument to network message
	nm := msg.(NetworkMsg)
	classID := nm.ClassID

	// make sure that this classID is represented in the interface
	intrfc.State.priQueue.addClassID(classID, false)

	// message length in Mbits
	msgLen := float64(8*nm.MsgLen) / 1e+6

	nowInSecs := evtMgr.CurrentSeconds()

	// record arrival
	intrfc.addTrace("enterEgressIntrfc", &nm, nowInSecs)

	AddIntrfcTrace(devTraceMgr, evtMgr.CurrentTime(), nm.ExecID, intrfc.Number, "arriveEgress", intrfc.State.priQueue.Str())

	// see if a frame is being transmitted right now
	if !intrfc.State.EgressTransit {
		// no frames in transit through the interface so we can start one
		schedTransmissionComplete(evtMgr, intrfc, nm, msgLen)
	} else {
		// put the frame in queue to await selection
		cpyNm := new(NetworkMsg)
		*cpyNm = nm
		intrfc.State.priQueue.addNetworkMsg(cpyNm, false)
	}
	return nil
}

// exitEgressIntrfc implements an event handler for the completed departure of a message from an interface.
// It determines the time-through-network and destination of the message, and schedules the recognition
// of the message at the ingress interface
func exitEgressIntrfc(evtMgr *evtm.EventManager, egressIntrfc any, msg any) any {
	intrfc := egressIntrfc.(*intrfcStruct)
	nm := msg.(NetworkMsg)

	// express message length in Mbits
	msgLen := float64(8*nm.MsgLen) / 1e6

	// compute transmission time through the interface
	// departSrv := (float64(8*nm.MsgLen)/1e+6)/intrfc.State.Bndwdth
	nxtIntrfc := IntrfcByID[(*nm.Route)[nm.StepIdx].dstIntrfcID]

	// create a vrtime representation of when service is completed that starts now
	// vtDepartTime := vrtime.SecondsToTime(evtMgr.CurrentSeconds()+departSrv)
	vtDepartTime := vrtime.SecondsToTime(evtMgr.CurrentSeconds())

	// record that time in logs
	intrfc.PrmDev.LogNetEvent(vtDepartTime, &nm, "exit")
	intrfc.LogNetEvent(vtDepartTime, &nm, "exit")

	AddIntrfcTrace(devTraceMgr, evtMgr.CurrentTime(), nm.ExecID, intrfc.Number, "exitEgress", intrfc.State.priQueue.Str())

	// remember that we visited
	intrfc.addTrace("exitEgressIntrfc", &nm, evtMgr.CurrentSeconds())

	netDelay, net := transitDelay(&nm)

	// log the completed departure
	net.LogNetEvent(vtDepartTime, &nm, "enter")

	// keep track of the least amount of non-flow bandwidth encountered by a packet
	nm.PcktRate = math.Min(nm.PcktRate, intrfc.State.Bndwdth-intrfc.State.EgressLambda)
	nm.PcktRate = math.Min(nm.PcktRate, net.NetState.Bndwdth-(intrfc.State.EgressLambda+nxtIntrfc.State.IngressLambda))

	// transitDelay will differentiate between point-to-point wired connection and passage through a network.
	// From the time of enterEgressIntrfc, the time of the first bit of the frame to reach dstIntrfc
	// is msgLen/minBW + delay, and the time when the last bit passes through dstIntrfc is
	// msgLen/minBW + delay + msgLen/minBW.  The time when enterEgressIntrfc was entered was
	// msgLen/minBW ago, so netDelay + msgLen/minBW is the time when the last bit passes through
	// the next ingress interface
	// recover bandwidth used to push frame out (no faster than can be received)
	thruIntrfc := msgLen / intrfc.State.end2endBW

	// what we schedule depends on whether the destination device is simple or not
	if nxtIntrfc.Device.DevIsSimple() {
		evtMgr.Schedule(nxtIntrfc, nm, arriveSimpleDev, vrtime.SecondsToTime(netDelay))
	} else {
		evtMgr.Schedule(nxtIntrfc, nm, arriveIngressIntrfc, vrtime.SecondsToTime(netDelay+thruIntrfc))
	}

	// we're done with the completed transmission
	intrfc.State.EgressTransit = false
	// sample the probability of a successful transition and packet drop
	// potentially drop the message
	if intrfc.Cable == nil || intrfc.Media != Wired {

		// get aggregate accepted arrival rate of flows from within the device to the egress interface
		lambda := intrfc.State.EgressLambda

		// message size in units of Mbits
		m := float64(msgLen*8) / 1e+6

		// estimated number of messages of this size that 'fit' in the network channel with
		// a time length of netDelay
		N := int(math.Round(netDelay * lambda * 1e+6 / m))

		// estimate packet loss via M/M/1 formula (N.B. need to change this to M/D/1 queue formula)
		prDrop := estPrDrop(lambda, net.NetState.Bndwdth, N)
		nm.PrArrvl *= (1.0 - prDrop)

		// if configured to drop packets, sample a uniform 0,1, if less than prDrop then drop the packet
		if net.PcktDrop() && (intrfc.Device.DevRng().RandU01() < prDrop) {
			// dropping packet
			// there is some logic for dealing with packet loss
			return nil
		}
	}

	// if there is another message awaiting at the entrance pull it in
	nxtMsg := intrfc.State.priQueue.nxtNetworkMsg(intrfc, false)
	if nxtMsg != nil {
		schedTransmissionComplete(evtMgr, intrfc, nm, msgLen)
	}

	// event-handlers are required to return _something_
	return nil
}

func schedTransmissionComplete(evtMgr *evtm.EventManager, intrfc *intrfcStruct, nm NetworkMsg, msgLen float64) {

	priQueue := intrfc.State.priQueue

	// get the local bandwidth (slowed by higher priority flow rates)
	egressBW := priQueue.transferBW(nm.ClassID, msgLen, intrfc, false)

	// get the available bandwidth at the destination interface
	dstBW := dstBndwdth(&nm)

	// take the minimum and remember it on this interface
	minBW := math.Min(egressBW, dstBW)
	intrfc.State.end2endBW = minBW

	// the local interface won't push it out any faster than the destination will absorb it
	// so we work with the minimum bandwidth of the two
	thruIntrfc := msgLen / minBW

	// schedule exitEgressIntrfc to execute after the frame has been transmitted
	evtMgr.Schedule(intrfc, nm, exitEgressIntrfc, vrtime.SecondsToTime(thruIntrfc))

	// keep other transmissions from happening here
	intrfc.State.EgressTransit = true
}

// arriveSimple handles the arrival of a message at a switch or router that is 'simple',
// meaning its delay is just some measurement that is called up for use
func arriveSimpleDev(evtMgr *evtm.EventManager, ingressIntrfc any, msg any) any {
	nm := msg.(NetworkMsg)
	intrfc := ingressIntrfc.(*intrfcStruct)
	intrfc.LogNetEvent(evtMgr.CurrentTime(), &nm, "enter")
	intrfc.PrmDev.LogNetEvent(evtMgr.CurrentTime(), &nm, "enter")
	AddIntrfcTrace(devTraceMgr, evtMgr.CurrentTime(), nm.ExecID, intrfc.Number, "arriveSimpleDev",
		intrfc.State.priQueue.Str())
	netDevType := intrfc.Device.DevType()

	// if the device is an endpoint deliver the message
	device := intrfc.Device
	devCode := device.DevType()
	if devCode == EndptCode {
		// schedule return into comp pattern system, where requested
		ActivePortal.Depart(evtMgr, nm)
		return nil
	}

	// estimate the probability of dropping the packet on the way out
	if netDevType == RouterCode || netDevType == SwitchCode {
		nxtIntrfc := IntrfcByID[(*nm.Route)[nm.StepIdx+1].srcIntrfcID]
		buffer := nxtIntrfc.State.BufferSize                     // buffer size in Mbytes
		N := int(math.Round(buffer * 1e+6 / float64(nm.MsgLen))) // buffer length in Mbytes
		lambda := intrfc.State.IngressLambda
		prDrop := estPrDrop(lambda, intrfc.State.Bndwdth, N)
		nm.PrArrvl *= (1.0 - prDrop)
	}

	// get the simple delay through the device
	delay := device.DevDelay(msg, nm.MsgLen)

	// get the delay through the network
	netDelay, _ := transitDelay(&nm)

	// advance along the route to the
	nm.StepIdx += 1
	nxtIntrfc := IntrfcByID[(*nm.Route)[nm.StepIdx].dstIntrfcID]

	// the handler we schedule depends on whether the device is simple or not
	if nxtIntrfc.Device.DevIsSimple() {
		evtMgr.Schedule(nxtIntrfc, nm, arriveSimpleDev, vrtime.SecondsToTime(delay+netDelay))
		return nil
	}

	// next device is not simple so we include a bandwidth limiting component
	dstBW := dstBndwdth(&nm)
	egressBW := nxtIntrfc.State.Bndwdth - nxtIntrfc.State.BckgrndBW
	minBW := math.Min(egressBW, dstBW)
	thruIntrfc := float64(8*nm.MsgLen) / (minBW * 1e6)
	evtMgr.Schedule(nxtIntrfc, nm, arriveIngressIntrfc, vrtime.SecondsToTime(delay+netDelay+thruIntrfc))
	return nil
}

// arriveIngressIntrfc implements the event-handler for the entry of a frame
// to an interface through which the frame will pass on its way out of a device. The
// event time is the completed arrival of the frame. Its main role is to add
// the switch/route time through the edge's departure and present to the egress interface.
func arriveIngressIntrfc(evtMgr *evtm.EventManager, ingressIntrfc any, msg any) any {
	// cast context argument to interface
	nm := msg.(NetworkMsg)

	intrfc := ingressIntrfc.(*intrfcStruct)
	intrfc.LogNetEvent(evtMgr.CurrentTime(), &nm, "enter")
	intrfc.PrmDev.LogNetEvent(evtMgr.CurrentTime(), &nm, "enter")
	AddIntrfcTrace(devTraceMgr, evtMgr.CurrentTime(), nm.ExecID, intrfc.Number, "enterIngress", intrfc.State.priQueue.Str())
	netDevType := intrfc.Device.DevType()
	msgLen := float64(nm.MsgLen)

	// cast data argument to network message

	nowInSecs := evtMgr.CurrentSeconds()

	intrfc.addTrace("arriveIngressIntrfc", &nm, nowInSecs)

	// if this device is an endpoint, the message gets passed up to it
	device := intrfc.Device
	devCode := device.DevType()
	devName := device.DevName()
	if devCode == EndptCode {
		// schedule return into comp pattern system, where requested
		ActivePortal.Depart(evtMgr, nm)
		return nil
	}

	// estimate the probability of dropping the packet on the way out
	if netDevType == RouterCode || netDevType == SwitchCode {
		nxtIntrfc := IntrfcByID[(*nm.Route)[nm.StepIdx+1].srcIntrfcID]
		buffer := nxtIntrfc.State.BufferSize                     // buffer size in Mbytes
		N := int(math.Round(buffer * 1e+6 / float64(nm.MsgLen))) // buffer length in Mbytes
		lambda := intrfc.State.IngressLambda
		prDrop := estPrDrop(lambda, intrfc.State.Bndwdth, N)
		nm.PrArrvl *= (1.0 - prDrop)
	}

	// if the device (switch or router) is simple, add a delay and have the
	// message show up at exitEgressIntrfc
	delay := 0.0
	// schedule arrival at the egress interface after the switching or routing time
	if devCode == SwitchCode {
		model := SwitchDevByName[devName].SwitchModel
		delayModel := devExecTimeTbl["switch"][model]
		delay = delayModel.b + delayModel.m*msgLen
	} else if devCode == RouterCode {
		model := RouterDevByName[devName].RouterModel
		delayModel := devExecTimeTbl["route"][model]
		delay = delayModel.b + delayModel.m*msgLen
	}

	// advance position along route
	nm.StepIdx += 1
	nxtIntrfc := IntrfcByID[(*nm.Route)[nm.StepIdx].srcIntrfcID]
	evtMgr.Schedule(nxtIntrfc, nm, enterEgressIntrfc, vrtime.SecondsToTime(delay))
	return nil
}

// estMM1NFull estimates the probability that an M/M/1/N queue is full.
// formula needs only the server utilization u, and the number of jobs in system, N
func estMM1NFull(u float64, N int) float64 {
	prFull := 1.0 / float64(N+1)
	if math.Abs(1.0-u) < 1e-3 {
		prFull = (1 - u) * math.Pow(u, float64(N)) / (1 - math.Pow(u, float64(N+1)))
	}
	return prFull
}

// estPrDrop estimates the probability that a packet is dropped passing through a network. From the
// function arguments it computes the server utilization and the number of
// messages that can arrive in the delay time, computes the probability
// of being in the 'full' state and returns that
func estPrDrop(rate, capacity float64, N int) float64 {
	// compute the ratio of arrival to service rate
	u := rate / capacity

	// estimate number of packets that can be served as the
	// number that can be present over the latency time,
	// given the rate and message length.
	//
	// in Mbits a packet is 8 * msgLen bytes / (1e+6 bits/Mbbit) = (8*m/1e+6) Mbits
	//
	// with a bandwidth of rate Mbits/sec, the rate in pckts/sec is
	//
	//        Mbits
	//   rate ------
	//         sec                      rate     pckts
	//  -----------------------  =     ------    ----
	//                 Mbits         (8*m/1e+6)   sec
	//    (8*m/1e+6)  ------
	//                  pckt
	//
	// The number of packets that can be accepted at this rate in a period of L secs is
	//
	//  L * rate
	//  -------- pckts
	//  (8*m/1e+6)
	//
	return estMM1NFull(u, N)
}

// passThruDelay returns the time it takes a device
// to perform its operation (switch or route)
func passThruDelay(opType, model string, msgLen int) float64 {
	if len(model) == 0 {
		model = "Default"
	}

	// if we don't have an entry for this operation, complain
	_, present := devExecTimeTbl[opType]
	if !present {
		panic(fmt.Errorf("no timing information for op type %s", opType))
	}

	// look up the execution time for the named operation using the name
	delayModel := devExecTimeTbl[opType][model]
	return delayModel.b + delayModel.m*float64(msgLen)
}

// FrameSizeCache holds previously computed minimum frame size along the route
// for a flow whose ID is the index
var FrameSizeCache map[int]int = make(map[int]int)

// FindFrameSize traverses a route and returns the smallest MTU on any
// interface along the way.  This defines the maximum frame size to be
// used on that route.
func FindFrameSize(frameID int, rt *[]intrfcsToDev) int {
	_, present := FrameSizeCache[frameID]
	if present {
		return FrameSizeCache[frameID]
	}
	frameSize := 1500
	for _, step := range *rt {
		srcIntrfc := IntrfcByID[step.srcIntrfcID]
		srcFrameSize := srcIntrfc.State.MTU
		if srcFrameSize > 0 && srcFrameSize < frameSize {
			frameSize = srcFrameSize
		}
		dstIntrfc := IntrfcByID[step.dstIntrfcID]
		dstFrameSize := dstIntrfc.State.MTU
		if dstFrameSize > 0 && dstFrameSize < frameSize {
			frameSize = dstFrameSize
		}
	}
	FrameSizeCache[frameID] = frameSize
	return frameSize
}

// LimitingBndwdth gives the amount of available bandwidth along
// the path between source and destination devices that has not already
// been allocated for flows
func LimitingBndwdth(srcDevName, dstDevName string) float64 {
	srcID := EndptDevByName[srcDevName].EndptID
	dstID := EndptDevByName[dstDevName].EndptID
	rt := findRoute(srcID, dstID)
	minBndwdth := math.MaxFloat64 / 2.0

	for _, step := range *rt {
		srcIntrfc := IntrfcByID[step.srcIntrfcID]
		dstIntrfc := IntrfcByID[step.dstIntrfcID]
		ingressEbndwdth := 0.0
		egressEbndwdth := 0.0

		for flowID, rate := range srcIntrfc.State.ToIngress {
			if ActivePortal.Elastic[flowID] {
				ingressEbndwdth += rate
			}
		}

		for flowID, rate := range dstIntrfc.State.ToEgress {
			if ActivePortal.Elastic[flowID] {
				egressEbndwdth += rate
			}
		}

		fixedIngressBndwdth := srcIntrfc.State.EgressLambda - egressEbndwdth
		// include pre-consumed background traffic
		fixedIngressBndwdth += srcIntrfc.State.BckgrndBW

		fixedEgressBndwdth := dstIntrfc.State.IngressLambda - ingressEbndwdth
		// include pre-consumed background traffic
		fixedEgressBndwdth += dstIntrfc.State.BckgrndBW

		minBndwdth = math.Min(minBndwdth, (1.0-srcIntrfc.State.RsrvdFrac)*srcIntrfc.State.Bndwdth-fixedIngressBndwdth)
		minBndwdth = math.Min(minBndwdth, (1.0-dstIntrfc.State.RsrvdFrac)*dstIntrfc.State.Bndwdth-fixedEgressBndwdth)
		minBndwdth = math.Max(0.0, math.Min(minBndwdth,
			srcIntrfc.Faces.NetState.Bndwdth-(fixedIngressBndwdth+fixedEgressBndwdth)))
	}
	return minBndwdth
}
