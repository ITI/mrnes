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
	"sort"
	"strings"
)


// intPair, intrfcIDPair, intrfcRate and floatPair are
// structs introduced to add more than basic elements to lists and maps
type intPair struct {
	i, j int
}

type intrfcIDPair struct {
	prevID, nextID int
	rate float64
}

type intrfcRate struct {
	intrfcID int
	rate float64
}

type floatPair struct {
	x, y float64
}

type classQueue struct {
	classID int
	ingressLambda float64	// sum of rates of flows in this class on the ingress side
	egressLambda float64	// sum of rates of flows in this class on the egress side
	inQueue	int				// number of enqueued packets
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
	_ = iota
	None FlowAction = iota
	Srt 
	Chg 
	End
)

var nmtToStr map[NetworkMsgType]string = map[NetworkMsgType]string{PacketType:"packet", 
	FlowType:"Flow"}

var routeStepIntrfcs map[intPair]intPair

// getRouteStepIntrfcs looks up the identity of the interfaces involved in connecting 
// the named source and the named destination.  These were previously built into a table
func getRouteStepIntrfcs(srcID, dstID int) (int, int) {
	ip := intPair{i: srcID, j: dstID}
	intrfcs, present := routeStepIntrfcs[ip]
	if !present {
		intrfcs, present = routeStepIntrfcs[intPair{i: dstID, j: srcID}]
		if !present {
			panic(fmt.Errorf("no step between %s and %s", TopoDevByID[srcID].DevName(), TopoDevByID[dstID].DevName()))
		}
	}
	return intrfcs.i, intrfcs.j
}

// NetworkPortal implements the pces interface used to pass
// traffic between the application layer and the network sim
type NetworkPortal struct {
	QkNetSim		bool
	ReturnTo		map[int]*rtnRecord
	LossRtn			map[int]*rtnRecord
	ReportRtnSrc	map[int]*rtnRecord
	ReportRtnDst	map[int]*rtnRecord
	RequestRate		map[int]float64		// flowID to arrival rate
	AcceptedRate	map[int]float64		// flowID to arrival rate
	Class			map[int]int			// flowID to priority class
	Connections		map[int]int
	InvConnection	map[int]int
	FlowCoeff		map[int]PerfCoeff
}

// ActivePortal remembers the most recent NetworkPortal created
// (there should be only one call to CreateNetworkPortal...)
var ActivePortal *NetworkPortal

type ActiveRec struct {
	Number int
	Rate float64
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
	np.QkNetSim = true
	np.ReturnTo = make(map[int]*rtnRecord)
	np.LossRtn = make(map[int]*rtnRecord)
	np.ReportRtnSrc = make(map[int]*rtnRecord)
	np.ReportRtnDst = make(map[int]*rtnRecord)
	np.RequestRate	= make(map[int]float64)
	np.AcceptedRate = make(map[int]float64)
	np.Class		= make(map[int]int)
	np.Connections	= make(map[int]intPair)
	np.InvConnection = make(map[int]int)
	np.FlowCoeff	= make(map[int]PerfCoeff)
	np.Class = make(map[int]float64)
	ActivePortal = np

	return np
}

// SetQkNetSim saves the argument as indicating whether latencies
// should be computed as 'Placed', meaning constant, given the state of the network at the time of 
// computation
func (np *NetworkPortal) SetQkNetSim(quick bool) {
	np.QkNetSim = quick
}

// ClearRates removes entries from maps to help manage space consumption
func (np *NetworkPortal) ClearRates(flowID int) {
	delete(np.RequestRate, flowID)
	delete(np.AcceptedRate, flowID)
	delete(np.MajorFlowPortal, flowID)
}

func (np *NetworkPortal) ClearConn(flowID int) {
	connectID = np.InvConnection[flowID]
	delete(np.ReturnTo, connectID)
	delete(np.LossRtn, connectID)
	delete(np.ReportRtnSrc, connectID)
	delete(np.ReportRtnDst, connectID)
}

// EndptCPUModel helps NetworkPortal implement the pces NetworkPortal interface,
// returning the CPU model associated with a named endpt.  Present because the
// application layer does not otherwise have visibility into the network topology
func (np *NetworkPortal) EndptCPUModel(devName string) string {
	endpt, present := EndptDevByName[devName]
	if present {
		return endpt.EndptModel
	}
	return ""
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
	rtnMsg.Rate = nm.SrvRate
	rtnMsg.PrLoss = (1.0-prArrvl)
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

// RtnMsgStruct formats the report passed from the network to the
// application calling it
type RtnMsgStruct struct {
	Latency float64		// span of time (secs) from srcDev to dstDev
	Rate    float64		// minimum available bandwidth encountered during transit
	PrLoss  float64		// estimated probability of having been dropped somewhere in transit
	Msg		any			// msg introduced at EnterNetwork
}


// Arrive is called at the point an application message is received by the network
// and a new connectID is created to track it.  It saves information needed to re-integrate
// the application message into the application layer when the message arrives at its destination
func (np *NetworkPortal) Arrive(rtns RtnDescs, frames int) int {

	rtnRec := &rtnRecord{rtnCxt: rtns.Rtn.Cxt, rtnFunc: rtns.Rtn.EvtHdlr, prArrvl: 1.0, pckts: frames}
	connectID := nxtConnectID()
	np.ReturnTo[connectID] = rtnRec

	if rtns.Src != nil {
		rtnRec = new(rtnRecord)
		*rtnRec = rtnRecord{rtnCxt: rtns.Src.Cxt, rtnFunc: rtns.Src.EvtHdlr, prArrvl: 1.0, pckts: 1}
		np.ReportRtnSrc[connectID] = rtnRec
	}

	if rtns.Dst != nil {
		rtnRec = new(rtnRecord)
		*rtnRec = rtnRecord{rtnCxt: rtns.Dst.Cxt, rtnFunc: rtns.Dst.EvtHdlr, prArrvl: 1.0, pckts: 1}
		np.ReportRtnDst[connectID] = rtnRec
	}

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
	delete(np.ReturnTo, connectID)

	_, present = np.LossRtn[connectID]
	if !present {
		return
	}

	lossRec := np.LossRtn[connectID]
	lossCxt := lossRec.rtnCxt
	lossFunc := lossRec.rtnFunc

	// schedule the re-integration into the application simulator
	evtMgr.Schedule(lossCxt, nm.Msg, lossFunc, vrtime.SecondsToTime(0.0))

	// remove lossRtn entry
	delete(np.LossRtn, connectID)
}

// devCode is the base type for an enumerated type of network devices
type DevCode int
const (
	EndptCode DevCode = iota
	SwitchCode
	RouterCode
	UnknownCode
)

// devCodefromStr returns the devCode corresponding to an string name for it
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

// devCodeToStr returns a string corresponding to an input devCode for it
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

// networkScale is the base type for an enumerated type of network type descriptions
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

// netScaleToStr returns a string name that corresponds to an input networkScale
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

// networkMedia is the base type for an enumerated type of comm network media
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
var connectID int = 0
func nxtConnectID() int {
	connectID += 1
	return connectID
}


type DFS map[int]intrfcIDPair

// the topDev interface specifies the functionality different device types provide
type TopoDev interface {
	DevName() string              // every device has a unique name
	DevID() int                   // every device has a unique integer id
	DevType() DevCode             // every device is one of the devCode types
	DevIntrfcs() []*intrfcStruct  // we can get from devices a list of the interfaces they endpt, if any
	DevDelay(any) float64         // every device can be be queried for the delay it introduces for an operation
	DevState() any                // every device as a structure of state that can be accessed
	DevRng() *rngstream.RngStream // every device has its own RNG stream
	DevAddActive(*NetworkMsg)     // add the connectID argument to the device's list of active connections
	DevRmActive(int)              // remove the connectID argument to the device's list of active connections
	DevForward() DFS			  // index by MajorID, yields map of ingress intrfc ID to egress intrfc ID
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
	Carry    *intrfcStruct   // points to the "other" interface in a connection
	Cable    *intrfcStruct   // For a wired interface, points to the "other" interface in the connection
	Wireless []*intrfcStruct // For a wired interface, points to the "other" interface in the connection
	Faces    *networkStruct  // pointer to the network the interface interacts with
	State    *intrfcState    // pointer to the interface's block of state information
}

// The  intrfcState holds parameters descriptive of the interface's capabilities
type intrfcState struct {
	Bndwdth     float64         // maximum bandwidth (in Mbytes/sec)
	BufferSize  float64         // buffer capacity (in Mbytes)
	Latency     float64         // time the leading bit takes to traverse the wire out of the interface
	Delay       float64         // time the leading bit takes to traverse the interface
	Empties     float64         // time when another packet can enter the egress side of the interface
	MTU         int             // maximum packet size (bytes)
	Trace       bool            // switch for calling add trace
	Drop		bool			// whether to permit packet drops

	ToIngress	map[int]float64
	ThruIngress	map[int]float64
	ToEgress	map[int]float64
	ThruDgress	map[int]float64

	IngressLoad	float64		// sum of rates of flows approach interface from ingress side. 
	EgressLoad	float64		// sum of rates of flows approach interface from egress side

	ClassQueue	map[int]classQueue	// mapID to (number in queue, departure time of last)
	IngressDepart	float64	
	EgressDepart	float64
}

// createIntrfcState is a constructor, assumes defaults on unspecified attributes
func createIntrfcState() *intrfcState {
	iss := new(intrfcState)
	iss.Delay = 1e+6     // in seconds!  Set this way so that if not initialized we'll notice
	iss.Latency = 1e+6
	iss.MTU = 1500			// in bytes Set for Ethernet2 MTU, should change if wireless

	iss.ToIngress		   = make(map[int]float64)
	iss.ThruIngress		   = make(map[int]float64)
	iss.ToEgress		   = make(map[int]float64)
	iss.ThruEgress		   = make(map[int]float64)

	iss.ClassQueue = make(map[int]classQueue)

	return iss
}


// computeWait estimates the time an arrival in classID is in the system,
// for either an ingress or egress interface.
// Formula for class k arrival is
//
//	W_k = \frac{R + \sum_{j=1}^{k-1} (rho_j*W_j) + D*\sum_{j=1}^{k} Q_j|}{1-\sum_{j=1}^k \rho_j}
//
//	where \rho_j = D*\lambda_j (utilization of class j), Q_j is number of class-j packets awaiting service, and
//	R is the (measured) residual service time
//
func (intrfc *intrfcStruct) computeWait(classID int, R float64, D float64, ingress bool) float64 {
	var agg float64
	idxs := int[]{}
	var allRho float64 

	for clsID, cg := range intrfc.ClassQueue {
		idxs = append(idxs, clsID)
		if ingress {
			allRho += cg.ingressLambda*D
		} else {
			allRho += cg.egressLambda*D
		}
	}

	sort.Ints(idxs)
	rbar := allRho*D/2.0

	rhoW := []float64{}
	rho  := []float64{}
 
	var rhoSum float64 
	var rhoWSum float64
	var qSum float64
	var W float64
	var rhoW float64

	for _, clsID := range idxs {
		if clsID == 0 {
			continue
		}
		qSum += float64(intrfc.ClassQueue[clsID])
		if ingress {
			rho     = cq.ingressLambda*D
			rhoSum += rho
		} else {
			rho     = cq.egressLambda*D
			rhoSum += rho
		}	
		W = (rbar + rhoWSum + D*qSum)/(1-rhoSum)
		rhoWSum += rho*W
	}
	return W
}

// createIntrfcStruct is a constructor, building an intrfcStruct from a desc description of the interface
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
	is.State = createIntrfcState()

	return is
}

func (intrfc *intrfcStruct) CummLambda(classID int, ingress bool) float64 {
	var agg float64 = 0.0
	var lambdaMap map[int]float64
	if ingress {
		lambdaMap = intrfc.IngressClassLambda
	} else {
		lambdaMap = intrfc.EgressClassLambda
	}

	for clsID, rate := range lambdaMap {
		if clsID != 0  && clsID <= classID {
			agg += rate
		}
	}
	
	if classID == 0 {
		agg += intrfc.IngressClassLambda[0]
	}
	return agg
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
	DevName string
	Faces string
	Bckgrnd float64
	ToIngress float64
	ThruIngress float64
	ToEgress float64
	ThruEgress float64
	MajorID int
	NetMsgType NetworkMsgType
	Rate float64
	PrArrvl float64
	Time float64
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
	if !intrfc.State.Trace {
		return
	}
	si := new(ShortIntrfc)
	si.DevName = intrfc.Device.DevName()
	si.Faces = intrfc.Faces.Name
	flwID := nm.MajorID
	si.MajorID = nm.MajorID
	si.Bckgrnd = 0.0
	si.ToIngress = intrfc.State.ToIngress[flwID]
	si.ThruIngress = intrfc.State.ThruIngress[flwID]
	si.ToEgress = intrfc.State.ToEgress[flwID]
	si.ThruEgress = intrfc.State.ThruEgress[flwID]

	si.NetMsgType = nm.NetMsgType	
	si.Rate = nm.SrvRate
	si.PrArrvl = nm.PrArrvl
	si.Time = t
	siStr := si.Serialize()
	siStr = strings.Replace(siStr,"\n"," ",-1)
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
	case "pcktclass":
		// fraction of interface bandwidth reserved for packet access
		intrfc.State.PcktClass = value.floatValue
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
		is.Cable = IntrfcByName[intrfcDesc.Cable]
	}

	// in IntrfcDesc the 'Cable' field is a string, holding the name of the target interface
	if len(intrfcDesc.Carry) > 0 {
		is.Carry = IntrfcByName[intrfcDesc.Carry]
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

// AddFlow initializes the To and Thru maps for the interface
func (intrfc *intrfcStruct) AddFlow(flowID int, classID int, ingress bool) {
	if ingress {
		intrfc.State.IngressClassID[flowID] = classID
		intrfc.State.ToIngress[flowID] = 0.0
		intrfc.State.ThruIngress[flowID] = 0.0
		_, present := intrfc.State.IngressClassFlows[classID]
		if !present {
			intrfc.State.IngressClassFlows[classID] = make([]int,0)
		}
		intrfc.State.IngressClassFlows[classID] = append(intrfc.State.IngressClassFlows[classID], flowID)
	} else {
		intrfc.State.EgressClassID[flowID] = classID
		intrfc.State.ToEgress[flowID] = 0.0
		intrfc.State.ThruEgress[flowID] = 0.0
		_, present := intrfc.State.EgressClassFlows[classID]
		if !present {
			intrfc.State.EgressClassFlows[classID] = make([]int,0)
		}
		intrfc.State.EgressClassFlows[classID] = append(intrfc.State.EgressClassFlows[classID], flowID)
	}
}

// IsCongested determines whether the interface is congested,
// meaning that the bandwidth used by major flows and reflected
// in the "To" maps is less than the interface's bandwidth.
// Note that even through there is a field in the interface holding
// the sum of rates going through the interface, that sum isn't correct
// in the midst of recalcating rates as flows come and go, and requested rates
// change
func (intrfc *intrfcStruct) IsCongested(ingress bool) bool {
	var usedBndwdth float64 = 0.0
	if ingress {
		for _, rate := range intrfc.State.ToIngress {
			usedBndwdth += rate
		}	
	} else {
		for _, rate := range intrfc.State.ToEgress {
			usedBndwdth += rate
		}
	}

	if usedBndwdth < intrfc.State.Bndwdth {
		return false
	}
	return true
}

// ChgFlowRate is called in the midst of changing the flow rates. 
// The rate value 'rate' is one that flow has at this point in the
// computation, and the per-flow interface data structures are adjusted
// to reflect that
func (intrfc *intrfcStruct) ChgFlowRate(flowID int, classID, rate float64, ingress bool ) {
	if ingress {
		oldRate := intrfc.State.ToIngress[flowID]
		intrfc.State.ToIngress[flowID] = rate
		intrfc.State.ThruIngress[flowID] = rate
		lambda, present := intrfc.State.IngressClassLambda[classID] 
		if present {
			intrfc.State.IngressClassLambda[classID] = lambda-(oldRate-rate)
		} else {
			intrfc.State.IngressClassLambda[classID] = rate
		}
	} else {
		oldRate := intrfc.State.Egress[flowID]
		intrfc.State.Egress[flowID] = rate
		intrfc.State.ThruEgress[flowID] = rate
		lambda, present := intrfc.State.EgressClassLambda[classID] 
		if present {
			intrfc.State.EgressClassLambda[classID] = lambda-(oldRate-rate)
		} else {
			intrfc.State.EgressClassLambda[classID] = rate
		}
	}
}


func (intrfc *intrfcStruct) RmFlow(flowID int, ingress bool) {
	if ingress {
		delete(intrfc.State.ToIngress, flowID)
		delete(intrfc.State.ThruIngress, flowID)
		class, present := intrfc.State.IngressClass[flowID]
		if present {
			delete(intrfc.State.IngressClass, flowID)
		}
	} else {
		delete(intrfc.State.ToEgress, flowID)
		delete(intrfc.State.ThruEgress, flowID)
		class, present := intrfc.State.EgressClass[flowID]
		if present {
			delete(intrfc.State.IngressClass, flowID)
		}
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

	ClassID	   map[int]int			// map of flow ID to reservation ID
	ClassBndwdth  map[int]float64	// map of reservation ID to reserved bandwidth
	
	PcktClass	 float64
	Bckgrnd  float64
	// revisit
	Flows    map[int]ActiveRec 
	Forward  map[int]map[intrfcIDPair]float64

	Load     float64           // real-time value of total load (in units of Mbytes/sec)
	Packets  int               // number of packets actively passing in network
}


// AddFlow updates a networkStruct's data structures to add
// a major flow
func (ns *networkStruct) AddFlow(flowID int, classID int, ifcpr intrfcIDPair) {
	_, present := ns.NetState.Flows[flowID]
	if !present {
		ns.NetState.Flows[flowID] = ActiveRec{Number:0, Rate: 0.0}
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

	class, present := ns.NetState.Class[flowID]
	if present {
		delete(ns.NetState.Class, flowID)
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
	deltaRate := rate-oldRate

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
	var load float64 = srcIntrfc.State.EgressLoad+dstIntrfc.State.IngressLoad
	return ns.NetState.Bndwdth <= load
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
func (ns *networkStruct) NetLatency(nm *NetworkMsg) float64 {
	// get the service rate for this message in this network
	mu := ns.ServiceRate(nm)

	thru := (float64(nm.MsgLen*8)/1e6)/mu

	return ns.NetState.Latency + thru
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
	case "capacity":
		ns.NetState.Capacity = fltValue
	case "pcktclass":
		ns.NetState.PcktClass = fltValue
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
func (ns *networkStruct) ServiceRate(nm *NetworkMsg) float64 {
	rtStep := (*nm.route)[nm.StepIdx]
	srcIntrfc := IntrfcByID[rtStep.srcIntrfcID]
	dstIntrfc := IntrfcByID[rtStep.dstIntrfcID]

	// if the message is in a reservation channel we need to
	// find the other flows in that channel passing through
	// the egress or ingress interface
	if nm.ClassID > 0 {
		rate, present := ns.ClassBndwdth[nm.ClassID]
		if present {
			var used float64
	
			// look at flows on the egress side
			for flwID, _ := range srcIntrfc.State.ThruEgress {
				classID, present = ns.ClassID[flwID]
				if present {
					used += ns.NetState.ClassBndwdth[classID]
				}
			}

			// now add contribution of flows on the ingress side
			for flwID, _ := range dstIntrfc.State.ToIngress {
				classID, present = ns.NetState.ClassID[flwID]
				if present {
					used += ns.NetState.ClassBndwdth[classID]
				}
			}
			// return unclaimed bandwidth
			return rate - used
		}
	} 
	// otherwise the service rate is the non-used portion of the network bandwidth
	return math.Max(ns.NetState.Bndwdth - (srcIntrfc.EgressLoad+dstIntrfc.IngressLoad), 0.0)
}


func (ns *networkStruct) PcktDrop() bool {
	return ns.NetState.Drop
}	

// a endptDev holds information about a endpt
type endptDev struct {
	EndptName    string   // unique name
	EndptGroups  []string // list of groups to which endpt belongs
	EndptModel   string   // model of CPU the endpt uses
	EndptCores   int
	EndptSched   *TaskScheduler  // shares an endpoint's cores among computing tasks
	EndptID      int             // unique integer id
	EndptIntrfcs []*intrfcStruct // list of network interfaces embedded in the endpt
	EndptState   *endptState     // a struct holding endpt state
}

// a endptState holds extra informat used by the endpt
type endptState struct {
	Rngstrm *rngstream.RngStream // pointer to a random number generator
	Trace   bool                 // switch for calling add trace
	Drop	bool				 // whether to support packet drops at interface
	Active  map[int]float64
	Load    float64
	Forward DFS
	Packets int
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
	endpt.EndptID = nxtID()                       // unique integer id, generated at model load-time
	endpt.EndptIntrfcs = make([]*intrfcStruct, 0) // initialization of list of interfaces, to be augmented later
	endpt.EndptGroups = endptDesc.Groups
	endpt.EndptState = createEndptState(endpt.EndptName) // creation of state block, to be augmented later
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
	return eps
}

func (endpt *endptDev) initTaskScheduler() {
	scheduler := CreateTaskScheduler(endpt.EndptCores)
	endpt.EndptSched = scheduler
	TaskSchedulerByHostName[endpt.EndptName] = scheduler
}

// addIntrfc appends the input intrfcStruct to the list of interfaces embedded in the endpt.
func (endpt *endptDev) addIntrfc(intrfc *intrfcStruct) {
	endpt.EndptIntrfcs = append(endpt.EndptIntrfcs, intrfc)
}

// rng resturns the string type description of the CPU model running the endpt
func (endpt *endptDev) CPUModel() string {
	return endpt.EndptModel
}

// devName returns the endpt name, as part of the TopoDev interface
func (endpt *endptDev) DevName() string {
	return endpt.EndptName
}

// devID returns the endpt integer id, as part of the TopoDev interface
func (endpt *endptDev) DevID() int {
	return endpt.EndptID
}

// devType returns the endpt's device type, as part of the TopoDev interface
func (endpt *endptDev) DevType() DevCode {
	return EndptCode
}

// devIntrfcs returns the endpt's list of interfaces, as part of the TopoDev interface
func (endpt *endptDev) DevIntrfcs() []*intrfcStruct {
	return endpt.EndptIntrfcs
}

// devState returns the endpt's state struct, as part of the TopoDev interface
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
	endpt.EndptState.Active[nme.ConnectID] = nme.SrvRate
}

// devRmActive removes an active connection, as part of the TopoDev interface.  Not used for endpts, yet
func (endpt *endptDev) DevRmActive(connectID int) {
	delete(endpt.EndptState.Active, connectID)
}

// devDelay returns the state-dependent delay for passage through the device, as part of the TopoDev interface.
// Not really applicable to endpt, so zero is returned
func (endpt *endptDev) DevDelay(arg any) float64 {
	return 0.0
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
	Rngstrm *rngstream.RngStream // pointer to a random number generator
	Trace   bool                 // switch for calling trace saving
	Drop    bool                 // switch to allow dropping packets
	Active  map[int]float64
	Load    float64
	BufferSize  float64
	Capacity float64
	Forward DFS
	Packets int
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

func (swtch *switchDev) DevForward() DFS {
	return swtch.SwitchState.Forward
}

func (swtch *switchDev) addForward(flowID int, idp intrfcIDPair) {
	swtch.SwitchState.Forward[flowID] = idp
}

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

// devName returns the switch name, as part of the TopoDev interface
func (swtch *switchDev) DevName() string {
	return swtch.SwitchName
}

// devID returns the switch integer id, as part of the TopoDev interface
func (swtch *switchDev) DevID() int {
	return swtch.SwitchID
}

// devType returns the switch's device type, as part of the TopoDev interface
func (swtch *switchDev) DevType() DevCode {
	return SwitchCode
}

// devIntrfcs returns the switch's list of interfaces, as part of the TopoDev interface
func (swtch *switchDev) DevIntrfcs() []*intrfcStruct {
	return swtch.SwitchIntrfcs
}

// devState returns the switch's state struct, as part of the TopoDev interface
func (swtch *switchDev) DevState() any {
	return swtch.SwitchState
}

// devRng returns the switch's rng pointer, as part of the TopoDev interface
func (swtch *switchDev) DevRng() *rngstream.RngStream {
	return swtch.SwitchState.Rngstrm
}

// DevAddActive adds a connection id to the list of active connections through the switch, as part of the TopoDev interface
func (swtch *switchDev) DevAddActive(nme *NetworkMsg) {
	swtch.SwitchState.Active[nme.ConnectID] = nme.SrvRate
}

// devRmActive removes a connection id to the list of active connections through the switch, as part of the TopoDev interface
func (swtch *switchDev) DevRmActive(connectID int) {
	delete(swtch.SwitchState.Active, connectID)
}

// devDelay returns the state-dependent delay for passage through the switch, as part of the TopoDev interface.
func (swtch *switchDev) DevDelay(msg any) float64 {
	delay := passThruDelay("switch", swtch.SwitchModel)
	// N.B. we could put load-dependent scaling factor here
	return delay
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
	Drop    bool				 // switch to allow dropping packets
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
	return router
}

func createRouterState(name string) *routerState {
	rs := new(routerState)
	rs.Active = make(map[int]float64)
	rs.Load = 0.0
	rs.Buffer = math.MaxFloat64/2.0
	rs.Packets = 0
	rs.Trace = false
	rs.Drop = false
	rs.Rngstrm = rngstream.New(name)
	rs.Forward = make(map[int]intrfcIDPair)
	return rs
}

func (rs *routerDev) DevForward() DFS {
	return rs.RouterState.Forward
}

func (router *routerDev) addForward(flowID int, idp intrfcIDPair) {
	router.RouterState.Forward[flowID] = idp
}

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

// devName returns the router name, as part of the TopoDev interface
func (router *routerDev) DevName() string {
	return router.RouterName
}

// devID returns the switch integer id, as part of the TopoDev interface
func (router *routerDev) DevID() int {
	return router.RouterID
}

// devType returns the router's device type, as part of the TopoDev interface
func (router *routerDev) DevType() DevCode {
	return RouterCode
}

// devIntrfcs returns the routers's list of interfaces, as part of the TopoDev interface
func (router *routerDev) DevIntrfcs() []*intrfcStruct {
	return router.RouterIntrfcs
}

// devState returns the routers's state struct, as part of the TopoDev interface
func (router *routerDev) DevState() any {
	return router.RouterState
}

// devRng returns a pointer to the routers's rng struct, as part of the TopoDev interface
func (router *routerDev) DevRng() *rngstream.RngStream {
	return router.RouterState.Rngstrm
}

func (router *routerDev) LogNetEvent(time vrtime.Time, nm *NetworkMsg, op string) {
	if !router.RouterState.Trace {
		return
	}
	AddNetTrace(devTraceMgr, time, nm, router.RouterID, op)
}

// DevAddActive includes a connectID as part of what is active at the device, as part of the TopoDev interface
func (router *routerDev) DevAddActive(nme *NetworkMsg) {
	router.RouterState.Active[nme.ConnectID] = nme.SrvRate
}

// devRmActive removes a connectID as part of what is active at the device, as part of the TopoDev interface
func (router *routerDev) DevRmActive(connectID int) {
	delete(router.RouterState.Active, connectID)
}

// devDelay returns the state-dependent delay for passage through the router, as part of the TopoDev interface.
func (router *routerDev) DevDelay(msg any) float64 {
	delay := passThruDelay("route", router.RouterModel)
	return delay
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
	Connection  ConnDesc 	   // {DiscreteConn, MajorFlowConn, MinorFlowConn}
	FlowID     int             // flow id given by app at entry
	ClassID    int			   // if > 0 means the message uses that reservation
	ConnectID  int             // connection identifier
	NetMsgType NetworkMsgType  // enum type packet, 
	PcktRate	float64          // if ClassID>0, the class arrival rate. Otherwise from source
	SrvRate   float64
	StartTime  float64
	PrArrvl    float64         // probablity of arrival
	MsgLen     int             // length of the entire message, in Mbytes
	PcktIdx	   int			   // index of packet with msg
	NumPckts   int			   // number of packets in the message this is part of
	Msg        any             // message being carried.
}

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

// The mrnsbit network simulator is built around three strong assumptions that
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
// pass through an interface instaneously, they 'connection' across links, through networks, and through devices.
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
// The third assumption is that for the life of a packet traversing from source endpt to destination endpt,
// the latencies across interfaces, wires, devices, and networks are constant.  This just means they
// don't change while the message is in motion, which means we can compute what the end-to-end latency will be.
// Latencies can be permitted to change in a state-dependent way, so the assumption is time-limited.
//
// With this understanding, the mrnsbit network simulator accepts a message carried in a cmpPtnMsg and
// creates two instances of the 'NetworkMsg' type. One represents the first bit of the message, the
// second the last bit.  The wrapper for the leading bit is inserted into the network, and the wrapper
// for the trailing bit is inserted at a time later equal to the time it would take the message to completely pass
// a point at the minimum rate described earlier.   Then the two NetworkMsg structs pass through
// every interface, link, network, and device, one following the other at a fixed delay in time, until
// the trailing edge struct is completely captured by the destination endpt.  Here (and then) the cmpPtnMsg
// is extracted and presented to the CompPattern Func scheduling apperatus.


// enterEgressIntrfc implements the event-handler for the entry of a message edge
// to an interface through which the message will pass on its way out of a device. The
// event time is when the specified bit of the network edge hits the interface.
// The main role of this handler is to compute and add the delay through the interface,
// mark its presence, and schedule the edge's departure
func enterEgressIntrfc(evtMgr *evtm.EventManager, egressIntrfc any, msg any) any {
	// cast context argument to interface
	intrfc := egressIntrfc.(*intrfcStruct)

	// cast data argument to network message
	nm := msg.(NetworkMsg)
	classID := nm.ClassID


	// message length in Mbits
	msgLen := float64(8*nm.MsgLen)/1e+6

	nowInSecs := evtMgr.CurrentSeconds()

	var delay float64
	intrfc.addTrace("enterEgressIntrfc", &nm, nowInSecs)

	// look up mean waiting time for this class here.
	// Compute actual residual
	resSrv := math.Max(0.0, evtMgr.CurrentSeconds()-intrfc.EgressDepart)
	delay := intrfc.computeWait(classID, resSrv , msgLen/intrfc.Bndwdth, false)

	// indicate the message is in its class's 'waiting for service' queue
	cq := intrfc.ClassQueue[msgClassID]
	cq.inQueue += 1
 
	// packet enters service at delay units of time after this point
	evtMgr.Schedule(egressIntrfc, nm, exitEgressIntrfc, vrtime.SecondsToTime(delay))

	// event-handlers are required to return _something_
	return nil
}



// exitEgressIntrfc implements an event handler for the departure of a message from an interface.
// It is called at the point a message enters service, which is when we decrement the number
// of packets of its class 

// It determines the time-through-network of the message and schedules the arrival
// of the message at the ingress interface
func exitEgressIntrfc(evtMgr *evtm.EventManager, egressIntrfc any, msg any) any {
	intrfc := egressIntrfc.(*intrfcStruct)
	nm := msg.(NetworkMsg)
	flowID := nm.FlowID
	classID := nm.ClassID

	// indicate the message has left the 'waiting for service' queue
	cq := intrfc.ClassQueue[classID]
	cq.inQueue -= 1

	departSrv := (float64(8*nm.MsgLen)/1e+6)/intrfcBndwdth
	nxtIntrfc := IntrfcByID[(*nm.Route)[nm.StepIdx].dstIntrfcID]

	departTime := evtMgr.CurrentTime()+departSrv
	intrfc.PrmDev.LogNetEvent(departTime, &nm, "exit")
	intrfc.LogNetEvent(departTime, &nm, "exit")
	intrfc.EgressDepart = departTime

	intrfc.addTrace("exitEgressIntrfc", &nm, evtMgr.CurrentSeconds())

	// transitDelay will differentiate between point-to-point wired connection and passage through a network
	netDelay, net := transitDelay(&nm)

	// N.B. return to pckt drop
	// sample the probability of a successful transition and packet drop
	// potentially drop the message
	if (intrfc.Cable == nil || intrfc.Media != Wired) {
		var arrivalRate float64
		cummLambda := intrfc.EgressCummLamba[flowID]
		m := float64(msgLen*8) / 1e+6
		N := int(math.Round(netDelay*cummLambda*1e+6/m))
		
		prDrop := estPrDrop(cummLambda, net.NetState.Bndwdth, N, cummLambda)
		nm.PrArrvl *= (1.0-prDrop)

		// sample a uniform 0,1, if less than prDrop then drop the packet
		if net.PcktDrop() && (intrfc.Device.DevRng().RandU01() < prDrop) {
			// dropping packet
			// there is some logic for dealing with packet loss	
			return nil
		}
	}

	net.LogNetEvent(evtMgr.CurrentTime(), &nm, "enter")

	// schedule arrival of the NetworkMsg at the next interface
	evtMgr.Schedule(nxtIntrfc, msg, enterIngressIntrfc, vrtime.SecondsToTime(netDelay))

	// event-handlers are required to return _something_
	return nil
}

// enterIngressIntrfc implements the event-handler for the entry of a message edge
// to an interface through which the message will pass on its way out of a device. The
// event time is bit of the network edge (0 or last) encoded in the networkStruct
// Its main role is to compute the message delay through the interface and schedule
// the edge's departure
func enterIngressIntrfc(evtMgr *evtm.EventManager, ingressIntrfc any, msg any) any {
	// cast context argument to interface
	intrfc := ingressIntrfc.(*intrfcStruct)

	// cast data argument to network message
	nm := msg.(NetworkMsg)
	ClassID := nm.ClassID
	msgLen := float64(nm.MsgLen*8)/1e6)

	netDevType := intrfc.Device.DevType()

	intrfc.LogNetEvent(evtMgr.CurrentTime(), &nm, "enter")
	intrfc.PrmDev.LogNetEvent(evtMgr.CurrentTime(), &nm, "enter")

	intrfc.addTrace("enterIngressIntrfc", &nm, evtMgr.CurrentSeconds())

	// estimate the probability of dropping the packet on the way out
	if (netDevType == RouterCode || netDevType == SwitchCode) {
		nxtIntrfc := IntrfcByID[(*nm.Route)[nm.StepIdx+1].srcIntrfcID]
		buffer := nxtIntrfc.State.BufferSize					// buffer size in Mbytes
		N := int(math.Round(buffer*1e+6/float64(nm.MsgLen)))    // buffer length in Mbytes
		cummLambda := intrfc.IngressCummLambda[ClassID]
		prDrop := estPrDrop(cummLambda, intrfc.State.Bndwdth, N)
		nm.PrArrvl *= (1.0-prDrop)
	}

	intrfc.State.Packets += 1
	if intrfc.Faces != nil {
		intrfc.Faces.NetState.Packets -= 1
	}

	// get delay through interface, including class-dependent waiting
	delay := intrfc.State.Waiting[classID] + intrfc.State.Delay + msgLen/intrfc.Bandwdth

	// schedule exit from this interface after msg passes through
	evtMgr.Schedule(ingressIntrfc, nm, exitIngressIntrfc, vrtime.SecondsToTime(delay))

	return nil
}

// exitIngressIntrfc is the event handler for the arrival of a message at an interface facing the connection
// through which the NetworkMsg arrived. When this event handler is called the entire
// msg is exiting. If this device is a endpt then accept the message
// and push it into the CompPattern Func scheduling system. Otherwise compute the time the edge hits
// the egress interface on the other side of device and schedule that arrival
func exitIngressIntrfc(evtMgr *evtm.EventManager, ingressIntrfc any, msg any) any {
	intrfc := ingressIntrfc.(*intrfcStruct)
	nm := msg.(NetworkMsg)

	// log passage of msg through the interface
	intrfc.LogNetEvent(evtMgr.CurrentTime(), &nm, "exit")

	intrfc.addTrace("exitIngressIntrfc", &nm, evtMgr.CurrentSeconds())
	intrfc.State.Packets -= 1

	// check whether to leave the network. N.B., passage through switches and routers are not
	// treated as leaving the network
	devCode := intrfc.Device.DevType()
	if devCode == EndptCode {
		// schedule return into comp pattern system, where requested
		ActivePortal.Depart(evtMgr, nm)
		return nil
	}

	// push the message through the device and to the exgress interface
	// look up minimal delay through the device, add time for the last bit of message to clear ingress interface
	thisDev := intrfc.Device
	delay := thisDev.DevDelay(nm)

	// add this message to the device's active map
	thisDev.DevAddActive(&nm)

	// advance position along route
	nm.StepIdx += 1
	nxtIntrfc := IntrfcByID[(*nm.Route)[nm.StepIdx].srcIntrfcID]
	evtMgr.Schedule(nxtIntrfc, nm, enterEgressIntrfc, vrtime.SecondsToTime(delay))

	// event scheduler has to return _something_
	return nil
}

// estMM1NFull estimates the probability that an M/M/1/N queue is full.
// formula needs only the server utilization u, and the number of jobs in system, N
func estMM1NFull(u float64, N int) float64 {
	prFull := 1.0 / float64(N + 1)
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
	u := load / capacity

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

// PerfCoeff holds values used to estimate the mean latency along a route,
// as a function of the constants and server utilizations along the way
type PerfCoeff struct {
	AggConst float64
	RhoVec []float64
}

// passThruDelay returns the time it takes a device 
// to perform its operation (switch or route)
func passThruDelay(opType, model string) float64 {

	/*
	// to look up the model we need to know whether this is a switch or router
	if dev.DevType() == RouterCode {
		rtrDev := dev.(*routerDev)
		model = rtrDev.RouterModel
	} else {
		switchDev := dev.(*switchDev)
		model = switchDev.SwitchModel
	}
	*/

	if len(model) == 0 {
		model = "Default"
	}

	// if we don't have an entry for this operation, complain
	_, present := devExecTimeTbl[opType]
	if !present {
		panic(fmt.Errorf("no timing information for op type %s", opType))
	}

	// look up the execution time for the named operation using the name
	return devExecTimeTbl[opType][model]
}


// FrameSizeCache holds previously computed minimum frame size along the route
// for a flow whose ID is the index
var FrameSizeCache map[int]int = make(map[int]int)

// FindFrameSiae traverses a route and returns the smallest MTU on any
// interface along the way.  This defines the maximum frame size to be
// used on that route.
func FindFrameSize(frameID int, rt *[]intrfcsToDev) int {
	_, present := FrameSizeCache[frameID]
	if present {
		return FrameSizeCache[frameID]
	}
	frameSize := 1500
	for _, step := range (*rt) {
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

// LimitingBndwdth gives the amount of unreserved bandwidth along
// the path between source and destination devices.  When the computation is
// performed as part of changing the rage of a reserved flow, the current reservation
// amount is included to be addeded back in as part of the analysis whether a requested
// flow change can be supported
func LimitingBndwdth(srcDevName, dstDevName string, restoreRate float64) float64 {
	srcID := EndptDevByName[srcDevName].EndptID
	dstID := EndptDevByName[dstDevName].EndptID
	rt := findRoute(srcID, dstID)
	minBndwdth := math.MaxFloat64/2.0

	for _, step := range (*rt) {
		srcIntrfc := IntrfcByID[step.srcIntrfcID]
		minBndwdth = math.Min(minBndwdth, srcIntrfc.State.Bndwdth+restoreRate)
		minBndwdth = math.Min(minBndwdth, srcIntrfc.Faces.NetState.Bndwdth+restoreRate)
		dstIntrfc := IntrfcByID[step.dstIntrfcID]
		minBndwdth = math.Min(minBndwdth, dstIntrfc.State.Bndwdth+restoreRate)
	}
	return minBndwdth
}

func AvailBndwdth(srcDevName, dstDevName string) float64 {
	srcID := EndptDevByName[srcDevName].EndptID
	dstID := EndptDevByName[dstDevName].EndptID
	rt := findRoute(srcID, dstID)
	minBndwdth := math.MaxFloat64/2.0

	for _, step := range (*rt) {
		srcIntrfc := IntrfcByID[step.srcIntrfcID]
		minBndwdth = math.Min(minBndwdth, srcIntrfc.State.Bndwdth-srcIntrfc.State.EgressLoad)	
		dstIntrfc := IntrfcByID[step.dstIntrfcID]
		net := srcIntrfc.Faces
		minBndwdth = math.Min(minBndwdth, net.NetState.Bndwdth-(srcIntrfc.State.EgressLoad+dstIntrfc.State.IngressLoad))
		minBndwdth = math.Min(minBndwdth, dstIntrfc.State.IngressLoad)
	}
	return minBndwdth
}



