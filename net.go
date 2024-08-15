package mrnes

// nets.go contains code and data structures supporting the
// simulation of traffic through the communication network.

import (
	"encoding/json"
	"fmt"
	"github.com/iti/evt/evtm"
	"github.com/iti/evt/vrtime"
	"github.com/iti/rngstream"
	"golang.org/x/exp/slices"
	"gopkg.in/yaml.v3"
	"math"
	"os"
	"path"
	"strings"
)

// a rtnRecord saves the event handling function to call when the network simulation
// pushes a message back into the application layer
type rtnRecord struct {
	count   int
	rtnFunc evtm.EventHandlerFunction
	rtnCxt  any
	rtnLoss *float64
	rtnBndwdth *float64
}

type intPair struct {
	i, j int
}

type floatPair struct {
	x, y float64
}

type NetworkMsgType int

const (
	packet NetworkMsgType = iota
	srtFlow
	endFlow
	flowRate
)

var routeStepIntrfcs map[intPair]intPair

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
	QkNetSim    bool
	ReturnTo    map[int]*rtnRecord
	LossRtn     map[int]*rtnRecord
}

// ActivePortal remembers the most recent NetworkPortal created
// (there should be only one call to CreateNetworkPortal...)
var ActivePortal *NetworkPortal

type activeRec struct {
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
	ActivePortal = np

	return np
}

// SetQkNetSim assigns a boolean to the network portal to indicate whether or not
// the 'quicksim' option should be applied
func (np *NetworkPortal) SetQkNetSim(qknetsim bool) {
	np.QkNetSim = qknetsim
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
	np.ReturnTo[connectID].count -= 1

	rtnMsg := new(RtnMsgStruct)
	rtnMsg.Latency = evtMgr.CurrentSeconds() - nm.StartTime
	rtnMsg.Bndwdth = nm.Rate
	rtnMsg.PrLoss = (1.0-nm.PrArrvl)
	rtnMsg.Msg = nm.Msg

	rtnRec := np.ReturnTo[connectID]
	rtnCxt := rtnRec.rtnCxt
	rtnFunc := rtnRec.rtnFunc

	// schedule the re-integration into the application simulator
	evtMgr.Schedule(rtnCxt, rtnMsg, rtnFunc, vrtime.SecondsToTime(0.0))
	if np.ReturnTo[connectID].count == 0 {
		delete(np.ReturnTo, connectID)
		delete(np.LossRtn, connectID)
	}
}

type RtnMsgStruct struct {
	Latency float64
	Bndwdth float64
	PrLoss  float64
	Msg		any
}

// Arrive is called at the point an application message is received by the network
// and a new connectID is created to track it.  It saves information needed to re-integrate
// the application message into the application layer when the message arrives at its destination
func (np *NetworkPortal) Arrive(rtnCxt any, rtnFunc evtm.EventHandlerFunction,
	lossCxt any, lossFunc evtm.EventHandlerFunction) int {
	rtnRec := &rtnRecord{count: 1, rtnFunc: rtnFunc, rtnCxt: rtnCxt}
	connectID := nxtConnectID()
	np.ReturnTo[connectID] = rtnRec
	lossRec := &rtnRecord{count: 1, rtnFunc: lossFunc, rtnCxt: lossCxt}
	np.LossRtn[connectID] = lossRec
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

// TraceRecord saves information about the visitation of a message to some point in the simulation.
// saved for post-run analysis
type TraceRecord struct {
	Time      float64 // time in float64
	Ticks     int64   // ticks variable of time
	Priority  int64   // priority field of time-stamp
	ExecID    int     // integer identifier identifying the chain of traces this is part of
	ConnectID int     // integer identifier of the network connection
	ObjID     int     // integer id for object being referenced
	Op        string  // "start", "stop", "enter", "exit"
	Packet    bool    // true if the event marks the passage of a packet (rather than flow)
	Rate      float64 // rate associated with the connection
}

// NameType is a an entry in a dictionary created for a trace
// that maps object id numbers to a (name,type) pair
type NameType struct {
	Name string
	Type string
}

// TraceManager implements the pces TraceManager interface. It is
// use to gather information about a simulation model and an execution of that model
type TraceManager struct {
	// experiment uses trace
	InUse bool `json:"inuse" yaml:"inuse"`

	// name of experiment
	ExpName string `json:"expname" yaml:"expname"`

	// text name associated with each objID
	NameByID map[int]NameType `json:"namebyid" yaml:"namebyid"`

	// all trace records for this experiment
	Traces map[int][]TraceRecord `json:"traces" yaml:"traces"`
}

// CreateTraceManager is a constructor.  It saves the name of the experiment
// and a flag indicating whether the trace manager is active.  By testing this
// flag we can inhibit the activity of gathering a trace when we don't want it,
// while embedding calls to its methods everywhere we need them when it is
func CreateTraceManager(ExpName string, active bool) *TraceManager {
	tm := new(TraceManager)
	tm.InUse = active
	tm.ExpName = ExpName
	tm.NameByID = make(map[int]NameType)    // dictionary of id code -> (name,type)
	tm.Traces = make(map[int][]TraceRecord) // traces have 'execution' origins, are saved by index to these
	return tm
}

// Active tells the caller whether the Trace Manager is activelyl being used
func (tm *TraceManager) Active() bool {
	return tm.InUse
}

// AddTrace creates a record of the trace using its calling arguments, and stores it
func (tm *TraceManager) AddTrace(vrt vrtime.Time, execID, connectID, objID int, op string,
	isPckt bool, rate float64) {

	// return if we aren't using the trace manager
	if !tm.InUse {
		return
	}

	_, present := tm.Traces[execID]
	if !present {
		tm.Traces[execID] = make([]TraceRecord, 0)
	}

	// create and add the trace record
	vmr := TraceRecord{Time: vrt.Seconds(), Ticks: vrt.Ticks(), Priority: vrt.Pri(), ConnectID: connectID,
		ExecID: execID, ObjID: objID, Op: op, Packet: isPckt, Rate: rate}

	tm.Traces[execID] = append(tm.Traces[execID], vmr)
}

// AddName is used to add an element to the id -> (name,type) dictionary for the trace file
func (tm *TraceManager) AddName(id int, name string, objDesc string) {
	if tm.InUse {
		_, present := tm.NameByID[id]
		if present {
			panic("duplicated id in AddName")
		}
		tm.NameByID[id] = NameType{Name: name, Type: objDesc}
	}
}

// WriteToFile stores the Traces struct to the file whose name is given.
// Serialization to json or to yaml is selected based on the extension of this name.
func (tm *TraceManager) WriteToFile(filename string) bool {
	if !tm.InUse {
		return false
	}
	pathExt := path.Ext(filename)
	var bytes []byte
	var merr error = nil

	if pathExt == ".yaml" || pathExt == ".YAML" || pathExt == ".yml" {
		bytes, merr = yaml.Marshal(*tm)
	} else if pathExt == ".json" || pathExt == ".JSON" {
		bytes, merr = json.MarshalIndent(*tm, "", "\t")
	}

	if merr != nil {
		panic(merr)
	}

	f, cerr := os.Create(filename)
	if cerr != nil {
		panic(cerr)
	}
	_, werr := f.WriteString(string(bytes[:]))
	if werr != nil {
		panic(werr)
	}
	f.Close()
	return true
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

var connectID int = 0

func nxtConnectID() int {
	connectID += 1
	return connectID
}

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
	LogNetEvent(vrtime.Time, int, int, string, bool, float64)
}

// ParamContainer interface is satisfied by every network object that
// can be configured at run-time with performance parameters. These
// are intrfcStruct, networkStruct, switchDev, endptDev, routerDev
type paramObj interface {
	matchParam(string, string) bool
	setParam(string, valueStruct)
	paramObjName() string
	LogNetEvent(vrtime.Time, int, int, string, bool, float64)
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
	Load        float64         // sum of rates passing through
	Latency     float64         // time the leading bit takes to traverse the wire out of the interface
	Delay       float64         // time the leading bit takes to traverse the interface
	Empties     float64         // time when another packet can enter the egress side of the interface
	MTU         int             // maximum packet size (bytes)
	Trace       bool            // switch for calling add trace
	Drop		bool			// whether to permit packet drops
	Active      map[int]float64 // id of a connection actively passing through the interface, and its bandwidth
	IngressLoad float64
	EgressLoad  float64
	Packets     int
}

func createIntrfcState() *intrfcState {
	iss := new(intrfcState)
	iss.Bndwdth = 0.0    // will be initialized or else we'll notice
	iss.BufferSize = 0.0 // not really using bufferSize yet
	iss.Delay = 1e+6     // in seconds!  Set this way so that if not initialized we'll notice
	iss.Latency = 1e+6
	iss.Empties = 0.0
	iss.MTU = 1500 // in bytes Set for Ethernet2 MTU, should change if wireless
	iss.Active = make(map[int]float64)
	iss.IngressLoad = 0.0
	iss.EgressLoad = 0.0
	iss.Packets = 0
	iss.Trace = false
	return iss
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

func (intrfc *intrfcStruct) Congested(ingress bool) bool {
	load := intrfc.State.IngressLoad
	if !ingress {
		load = intrfc.State.EgressLoad
	}
	return math.Abs(load-intrfc.State.Bndwdth) < 1e-3
}

type ShortIntrfc struct {
	DevName string
	Faces string
	IngressLoad float64
	EgressLoad float64
	ExecID int
	NetMsgType NetworkMsgType
	Rate float64
	PrArrvl float64
	Time float64
}

func (sis *ShortIntrfc) Serialize() string {
	var bytes []byte
	var merr error

	bytes, merr = yaml.Marshal(*sis)

	if merr != nil {
		panic(merr)
	}
	return string(bytes[:])
}


func (intrfc *intrfcStruct) addTrace(label string, nm *NetworkMsg, t float64) {
	if !intrfc.State.Trace {
		return
	}
	si := new(ShortIntrfc)
	si.DevName = intrfc.Device.DevName()
	si.Faces = intrfc.Faces.Name
	si.IngressLoad = intrfc.State.IngressLoad
	si.EgressLoad = intrfc.State.EgressLoad
	si.ExecID = nm.ExecID
	si.NetMsgType = nm.NetMsgType	
	si.Rate = nm.Rate
	si.PrArrvl = nm.PrArrvl
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
	case "buffer":
		// units of buffer are Mbytes
		intrfc.State.BufferSize = value.floatValue
	case "load":
		// units of buffer are Mbytes
		intrfc.State.IngressLoad = value.floatValue
		intrfc.State.EgressLoad = value.floatValue
	case "MTU":
		// number of bytes in maximally sized packet
		intrfc.State.MTU = value.intValue
	case "trace":
		intrfc.State.Trace = value.boolValue
	case "drop":
		intrfc.State.Drop = value.boolValue
	}
}

func (intrfc *intrfcStruct) LogNetEvent(time vrtime.Time, execID int, connectID int, op string, isPckt bool, rate float64) {
	if !intrfc.State.Trace {
		return
	}
	devTraceMgr.AddTrace(time, execID, connectID, intrfc.Number, op, isPckt, rate)
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

// availBndwdth returns the interface bandwidth available to a new connection
func (intrfc *intrfcStruct) AvailBndwdth(ingress bool) float64 {

	// N.B. is difference between ingress and egress loads important?
	if ingress {
		return math.Max(intrfc.State.Bndwdth-intrfc.State.IngressLoad, 0.0)
	} else {
		return math.Max(intrfc.State.Bndwdth-intrfc.State.EgressLoad, 0.0)
	}
}

func (intrfc *intrfcStruct) PcktDrop() bool {
	return intrfc.State.Drop
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
	Bndwdth  float64 // maximum bandwidth between any two routers in network
	Capacity float64 // maximum traffic capacity of network
	Trace    bool    // switch for calling trace saving
	Drop     bool    // switch for dropping packets with random sampling
	Rngstrm  *rngstream.RngStream
	Flows    map[int]activeRec // keep track of connections through the network
	Load     float64           // real-time value of total load (in units of Mbytes/sec)
	Packets  int               // number of packets actively passing in network
	
}

func (ns *networkStruct) AddFlow(execId int, rate float64) {
	_, present := ns.NetState.Flows[execId]
	if !present {
		ns.NetState.Flows[execId] = activeRec{Number:0, Rate:0.0}
	}
	ar := ns.NetState.Flows[execId]
	ar.Number += 1
	ar.Rate += rate
	ns.NetState.Flows[execId] = ar
	ns.NetState.Load += rate
	return
}

func (ns *networkStruct) RmFlow(execId int, rate float64) {
	_, present := ns.NetState.Flows[execId]
	if !present {
		panic(fmt.Errorf("tried to remove non-existant flow instance from network"))
	}
	ar := ns.NetState.Flows[execId]
	ar.Number -= 1
	ar.Rate -= rate
	ns.NetState.Flows[execId] = ar
	if ns.NetState.Flows[execId].Number == 0 {
		delete(ns.NetState.Flows, execId)
	}
	ns.NetState.Load -= rate
}
 
func (ns *networkStruct) ChgFlow(execId int, delta float64) {
	_, present := ns.NetState.Flows[execId]
	if !present {
		panic(fmt.Errorf("tried to remove non-existant flow instance from network"))
	}
	ar := ns.NetState.Flows[execId]
	ar.Rate += delta
	ns.NetState.Flows[execId] = ar
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
	ns.Flows = make(map[int]activeRec)
	ns.Load = 0.0
	ns.Packets = 0
	ns.Drop = false
	ns.Rngstrm = rngstream.New(name)
	return ns
}

// NetLatency estimates the time required by a message to traverse the network,
// optionally as a function of the load.  This approximation is the mean time in system
// for an M/M/1 queue: (1/mu)/(1-rho).

func (ns *networkStruct) NetLatency() float64 {
	rho := ns.NetState.Load / ns.NetState.Capacity
	rho = math.Min(rho, 0.99)
	m := ns.NetState.Latency / (1.0 - rho)
	return m
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
	case "load":
		ns.NetState.Load = fltValue
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

func (ns *networkStruct) LogNetEvent(time vrtime.Time, execID int, connectID int, op string, isPckt bool, rate float64) {
	if !ns.NetState.Trace {
		return
	}
	devTraceMgr.AddTrace(time, execID, connectID, ns.Number, op, isPckt, rate)
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

// netBndwdth returns the current bandwidth available for a new connection
func (ns *networkStruct) AvailBndwdth() float64 {
	return math.Max(ns.NetState.Bndwdth-ns.NetState.Load, 0.0)
}

func (ns *networkStruct) PcktDrop() bool {
	return ns.NetState.Drop
}	

// a endptDev holds information about a endpt
type endptDev struct {
	EndptName    string   // unique name
	EndptGroups  []string // list of groups to which endpt belongs
	EndptModel   string   // model of CPU the endpt uses
	EndptEUD     bool
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

// devRng returns the endpt's rng pointer, as part of the TopoDev interface
func (endpt *endptDev) DevRng() *rngstream.RngStream {
	return endpt.EndptState.Rngstrm
}

func (endpt *endptDev) LogNetEvent(time vrtime.Time, execID int, connectID int, op string, isPckt bool, rate float64) {

	if !endpt.EndptState.Trace {
		return
	}
	devTraceMgr.AddTrace(time, execID, connectID, endpt.EndptID, op, isPckt, rate)
}

// DevAddActive adds an active connection, as part of the TopoDev interface.  Not used for endpts, yet
func (endpt *endptDev) DevAddActive(nme *NetworkMsg) {
	endpt.EndptState.Active[nme.ConnectID] = nme.Rate
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
	return ss
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
	swtch.SwitchState.Active[nme.ConnectID] = nme.Rate
}

// devRmActive removes a connection id to the list of active connections through the switch, as part of the TopoDev interface
func (swtch *switchDev) DevRmActive(connectID int) {
	delete(swtch.SwitchState.Active, connectID)
}

// devDelay returns the state-dependent delay for passage through the switch, as part of the TopoDev interface.
func (swtch *switchDev) DevDelay(msg any) float64 {
	delay := passThruDelay(swtch, msg)
	// N.B. we could put load-dependent scaling factor here
	return delay
}

// LogNetEvent satisfies TopoDev interface
func (swtch *switchDev) LogNetEvent(time vrtime.Time, execID int, connectID int, op string, isPckt bool, rate float64) {
	if !swtch.SwitchState.Trace {
		return
	}
	devTraceMgr.AddTrace(time, execID, connectID, swtch.SwitchID, op, isPckt, rate)
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
	return rs
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

func (router *routerDev) LogNetEvent(time vrtime.Time, execID int, connectID int, op string, isPckt bool, rate float64) {
	if !router.RouterState.Trace {
		return
	}
	devTraceMgr.AddTrace(time, execID, connectID, router.RouterID, op, isPckt, rate)
}

// DevAddActive includes a connectID as part of what is active at the device, as part of the TopoDev interface
func (router *routerDev) DevAddActive(nme *NetworkMsg) {
	router.RouterState.Active[nme.ConnectID] = nme.Rate
}

// devRmActive removes a connectID as part of what is active at the device, as part of the TopoDev interface
func (router *routerDev) DevRmActive(connectID int) {
	delete(router.RouterState.Active, connectID)
}

// devDelay returns the state-dependent delay for passage through the router, as part of the TopoDev interface.
func (router *routerDev) DevDelay(msg any) float64 {
	delay := passThruDelay(router, msg)
	// N.B. we could put load-dependent scaling factor here

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
	ConnectID  int             // connection identifier
	ExecID     int             // execution id given by app at entry
	NetMsgType NetworkMsgType  // enum type packet, srtFlow, endFlow, flowRate
	Rate       float64         // effective bandwidth for message
	StartTime  float64
	PrArrvl    float64         // probablity of arrival
	MsgLen     int             // length of the entire message, in Mbytes
	Msg        any             // message being carried.
}

func (nm *NetworkMsg) carriesPckt() bool {
	return nm.NetMsgType == packet
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
		delay = net.NetLatency()
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

// EnterNetwork is called after the execution from the application layer
// It creates NetworkMsg structs to represent the start and end of the message, and
// schedules their arrival to the egress interface of the message source endpt
// func enterNetwork(evtMgr *evtm.EventManager, cpf cmpPtnFunc, cpm *cmpPtnMsg) any {
func (np *NetworkPortal) EnterNetwork(evtMgr *evtm.EventManager, srcDev, dstDev string, msgLen int,
	execID int, isPckt bool, flowState string, rate float64, msg any, rtnCxt any, rtnFunc evtm.EventHandlerFunction,
	lossCxt any, lossFunc evtm.EventHandlerFunction) any {

	if rtnCxt == nil {
		panic(fmt.Errorf("empty context given to EnterNetwork"))
	}

	srcID := TopoDevByName[srcDev].DevID()
	dstID := TopoDevByName[dstDev].DevID()

	// get the route from srcID to dstID
	route := findRoute(srcID, dstID)

	if route == nil || len(*route) == 0 {
		panic(fmt.Errorf("unable to find a route %s -> %s", srcDev, dstDev))
	}

	latency := float64(0.0)
	bndwdth := rate
	// if isPckt {
			// get the latency and bandwidth for traffic on this path, computed 'now'
	//		latency, bndwdth := routeTransitPerf(srcID, dstID, msg, route, true)
	// } 

	nMsgType := packet
	// determine whether this is a srtFlow, endFlow, or rate type flow message
	if !isPckt {
		if flowState == "srt" {
			nMsgType = srtFlow
			bndwdth = math.Min(bndwdth, rate)
		} else if flowState == "end" {
			nMsgType = endFlow
		} else if flowState == "chg" {
			nMsgType = flowRate
			bndwdth = math.Min(bndwdth, rate)
		}
	}

	// if the command line selected the -qnetsim option we skip the
	// detailed network simulation and jump straight to the receipt
	// of the last bit on exit of the interface at the endpt where the
	// message is routed
	if ActivePortal.QkNetSim {
		// covert the number of bytes pushing pushed into the number of Mbytes being pushed,
		// work out how long it takes for that many bytes to transit the pipe after the first bit,
		// and add to the network latency to determine when the last bit emerges
		// make it appear the connection entered the network
		connectID := np.Arrive(rtnCxt, rtnFunc, lossCxt, lossFunc)

		// data structure that describes the message, noting that the 'edge' is the last bit
		nm := NetworkMsg{StepIdx: len(*route) - 1, Route: route, Rate: bndwdth, PrArrvl: 1.0, MsgLen: msgLen,
			NetMsgType: nMsgType, ConnectID: connectID, ExecID: execID, Msg: msg, StartTime: evtMgr.CurrentSeconds()}

		// The destination interface of the last routing step is where the message ultimately emerges from the network.
		// That's the 'context' for the event-handler which deals with this data structure just as though it came off
		// the detailed network simulation
		ingressIntrfcID := (*route)[len(*route)-1].dstIntrfcID
		ingressIntrfc := IntrfcByID[ingressIntrfcID]

		// schedule exit from final interface after msg passes through
		evtMgr.Schedule(ingressIntrfc, nm, exitIngressIntrfc, vrtime.SecondsToTime(latency))

		return connectID
	}

	connectID := np.Arrive(rtnCxt, rtnFunc, lossCxt, lossFunc)

	// No quick network simulation, so make a message wrapper and push the message at the entry
	// of the endpt's egress interface

	nm := NetworkMsg{StepIdx: 0, Route: route, Rate: bndwdth, PrArrvl: 1.0, MsgLen: msgLen,
		NetMsgType: nMsgType, ConnectID: connectID, ExecID: execID, Msg: msg, StartTime: evtMgr.CurrentSeconds()}

	// get identity of egress interface
	intrfc := IntrfcByID[(*route)[0].srcIntrfcID]
	intrfc.Faces.LogNetEvent(evtMgr.CurrentTime(), nm.ExecID, nm.ConnectID, "enter", isPckt, bndwdth)

	// how long to get through the device to the interface?
	delay := (float64(msgLen*8) / 1e6) / bndwdth

	// schedule the entry of the leading NetworkMsg into the first egress interface (from the source endpt)
	// once the whole packet has connectioned through
	evtMgr.Schedule(intrfc, nm, enterEgressIntrfc, vrtime.SecondsToTime(delay))

	return connectID
}

var cachedConnectionPerf map[intPair]floatPair = make(map[intPair]floatPair)

func getCachedTransitPerf(srcID, dstID int) (bool, float64, float64) {
	var low, high int

	if srcID < dstID {
		low = srcID
		high = dstID
	} else {
		low = dstID
		high = srcID
	}

	index := intPair{i: low, j: high}
	fp, present := cachedConnectionPerf[index]
	if present {
		return true, fp.x, fp.y
	}
	return false, 0.0, 0.0
}

func setCachedTransitPerf(srcID, dstID int, latency, bndwdth float64) {
	var low, high int

	if srcID < dstID {
		low = srcID
		high = dstID
	} else {
		low = dstID
		high = srcID
	}

	index := intPair{i: low, j: high}
	values := floatPair{x: latency, y: bndwdth}
	cachedConnectionPerf[index] = values
}

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

	// remove the connection from the device
	thisDev := intrfc.Device
	thisDev.DevRmActive(nm.ExecID)

	nowInSecs := evtMgr.CurrentSeconds()
	nowInVTime := evtMgr.CurrentTime()
	var delay float64

	intrfc.addTrace("enterEgressIntrfc", &nm, nowInSecs)

	nm.Rate = math.Min(nm.Rate, intrfc.AvailBndwdth(false))

	// if this is a flow, mark that this connection is passing through the interface
	if !(nm.NetMsgType == packet) {
		intrfc.State.Active[nm.ExecID] = nm.Rate
		intrfc.LogNetEvent(nowInVTime, nm.ExecID, nm.ConnectID, "enter", false, nm.Rate)
		thisDev.LogNetEvent(nowInVTime, nm.ExecID, nm.ConnectID, "exit", false, nm.Rate)
	} else {
		// look up when the interface will be free to take this packet
		enterIntrfcTime := math.Max(nowInSecs, intrfc.State.Empties)

		// compute passage time based on bandwidth and message size
		msgLen := float64(nm.MsgLen*8) / 1e+6

		availbw := intrfc.AvailBndwdth(false)
		delay = math.MaxFloat64/2.0
		if availbw > 0.0 {
			delay = msgLen / availbw
		} 

		// remember when this packet clears the buffer.  Assumes FCFS queuing
		intrfc.State.Empties = enterIntrfcTime + delay
	}

	evtMgr.Schedule(egressIntrfc, nm, exitEgressIntrfc, vrtime.SecondsToTime(delay))

	// event-handlers are required to return _something_
	return nil
}

func estMM1NFull(u float64, N int) float64 {
	prFull := 1.0 / float64(N + 1)
	if math.Abs(1.0-u) < 1e-3 {
		prFull = (1 - u) * math.Pow(u, float64(N)) / (1 - math.Pow(u, float64(N+1)))
	}
	return prFull
}

func estPrDrop(load, capacity float64, msgLen int, delay, rate float64) float64 {
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
	// The number of packets that can be injected at this rate in a period of L secs is
	//
	//  L * rate
	//  -------- pckts
	//  (8*m/1e+6)
	//
	m := float64(msgLen*8) / 1e+6
	N := int(math.Round(delay*rate*1e+6/m))

	return estMM1NFull(u, N)
}

// exitEgressIntrfc implements an event handler for the departure of a message from an interface.
// It determines the time-through-network of the message and schedules the arrival
// of the message at the ingress interface
func exitEgressIntrfc(evtMgr *evtm.EventManager, egressIntrfc any, msg any) any {
	intrfc := egressIntrfc.(*intrfcStruct)
	nm := msg.(NetworkMsg)
	isPckt := (nm.NetMsgType == packet)

	intrfc.PrmDev.LogNetEvent(evtMgr.CurrentTime(), nm.ExecID, nm.ConnectID, "exit", isPckt, nm.Rate)
	intrfc.LogNetEvent(evtMgr.CurrentTime(), nm.ExecID, nm.ConnectID, "exit", isPckt, nm.Rate)

	intrfc.addTrace("exitEgressIntrfc", &nm, evtMgr.CurrentSeconds())

	// transitDelay will differentiate between point-to-point wired connection and passage through a network
	netDelay, net := transitDelay(&nm)

	rate := net.AvailBndwdth()
	nm.Rate = math.Min(rate, nm.Rate)

	// logic associated with flows
	if nm.NetMsgType == srtFlow {
		// include the flow in the network about to be entered.
		// Question, does that matter when the connection is wired?
		net.AddFlow(nm.ExecID, nm.Rate)
	} else if nm.NetMsgType == endFlow {
		// remove connection from those active on the interface
		_, present := intrfc.State.Active[nm.ExecID]
		if present {
			delete(intrfc.State.Active, nm.ExecID)
		}
	} else if nm.NetMsgType == flowRate {
		// a rateFlow message has entered before, but now the rate changes.

		// recover the old rate, subtract off from active and from load, and put in the new rate
		oldRate := intrfc.State.Active[nm.ExecID]
		net.ChgFlow(nm.ExecID, nm.Rate - oldRate)
		intrfc.State.EgressLoad -= (nm.Rate - oldRate) 	
	}

	// for a packet crossing a network, sample the probability of a successful transition and
	// potentially drop the message
	if isPckt && (intrfc.Cable == nil || intrfc.Media != Wired) {
		prDrop := estPrDrop(net.NetState.Load, net.NetState.Capacity, nm.MsgLen, netDelay, nm.Rate)
		nm.PrArrvl *= (1.0-prDrop)

		// sample a uniform 0,1, if less than prDrop then drop the packet
		if net.PcktDrop() && (intrfc.Device.DevRng().RandU01() < prDrop) {
			// dropping packet
			// there is some logic for dealing with packet loss	
			return nil
		}
	}

	net.LogNetEvent(evtMgr.CurrentTime(), nm.ExecID, nm.ConnectID, "enter", isPckt, nm.Rate)

	// schedule arrival of the NetworkMsg at the next interface
	nxtIntrfc := IntrfcByID[(*nm.Route)[nm.StepIdx].dstIntrfcID]
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
	isPckt := (nm.NetMsgType == packet)
	netDevType := intrfc.Device.DevType()

	nm.Rate = math.Min(nm.Rate, intrfc.AvailBndwdth(true))

	intrfc.LogNetEvent(evtMgr.CurrentTime(), nm.ExecID, nm.ConnectID, "enter", isPckt, nm.Rate)
	intrfc.PrmDev.LogNetEvent(evtMgr.CurrentTime(), nm.ExecID, nm.ConnectID, "enter", isPckt, nm.Rate)

	intrfc.addTrace("enterIngressIntrfc", &nm, evtMgr.CurrentSeconds())

	// estimate the probability of dropping the packet on the way out
	if isPckt && (netDevType == RouterCode || netDevType == SwitchCode) {
		nxtIntrfc := IntrfcByID[(*nm.Route)[nm.StepIdx+1].srcIntrfcID]
		load     := nxtIntrfc.State.EgressLoad
		capacity := nxtIntrfc.State.Bndwdth	
		buffer := nxtIntrfc.State.BufferSize         // buffer size in Mbytes
		N := int(math.Round(buffer*1e+6/float64(nm.MsgLen)))   // buffer length in Mbytes
		prDrop := estMM1NFull(load/capacity, N)
		nm.PrArrvl *= (1.0-prDrop)
	}

	// if the arrival is a packet look for congestion and drop the packet
	if intrfc.PcktDrop() && isPckt && intrfc.Congested(true) {
		ActivePortal.lostConnection(evtMgr, &nm, nm.ConnectID)
		return nil
	}

	if !isPckt {
		//  mark that connection is occupying interface
		oldRate := intrfc.State.Active[nm.ExecID] // notice that return is 0.0 if index not present
		intrfc.State.Active[nm.ExecID] = nm.Rate
		intrfc.State.IngressLoad += (nm.Rate - oldRate)

		if nm.NetMsgType == endFlow && intrfc.Faces != nil {
			intrfc.Faces.RmFlow(nm.ExecID, oldRate)
			delete(intrfc.State.Active, nm.ExecID)
		}
	} else {
		intrfc.State.Packets += 1
		if intrfc.Faces != nil {
			intrfc.Faces.NetState.Packets -= 1
		}
	}

	// get delay through interface
	delay := intrfc.State.Delay

	// schedule exit from this interface after msg passes through
	evtMgr.Schedule(ingressIntrfc, nm, exitIngressIntrfc, vrtime.SecondsToTime(delay))

	// event handlers are required to return _something_
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

	isPckt := (nm.NetMsgType == packet)

	// log passage of msg through the interface
	intrfc.LogNetEvent(evtMgr.CurrentTime(), nm.ExecID, nm.ConnectID, "exit", isPckt, nm.Rate)

	intrfc.addTrace("exitIngressIntrfc", &nm, evtMgr.CurrentSeconds())

	if isPckt {
		// take the connection off the interface active list
		intrfc.State.Packets -= 1
	} else if nm.NetMsgType == endFlow {
		// if the 'end' flag is set remove the flow rate from the interface
		intrfc.State.IngressLoad -= intrfc.State.Active[nm.ExecID]
		delete(intrfc.State.Active, nm.ExecID)
	}

	// log entry of packet into device
	//intrfc.PrmDev.LogNetEvent(evtMgr.CurrentTime(), nm.ExecID, nm.ConnectID, "enter", isPckt, nm.Rate)

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

// networkPerf computes estimated probability of packet loss and mean latency
// through the network as a function of load and capacity
func (ns *networkStruct) networkPerf(msgLen float64) (float64, float64) {

	// estimate packet loss probability
	rate := ns.NetState.Load
	a := rate / ns.NetState.Capacity

	netDelay := ns.NetState.Latency

	// estimate number of packets that can be served by
	//    sec           1            pckts
	//   ------ x -------------- x ----------
	//   Mbytes	   latency (sec)     Mbyte
	//
	// rate in Mbytes/sec, msgLen in Mbytes
	N := (1.0 / rate) / (netDelay * msgLen)

	// probability of being in state where a M/M/1/N queue is full
	//
	//  P_N = (1-a)a^N / (1-a^{N+1}) if a < 1
	//      = 1/(N+1) if a == 1

	prDrop := 1.0 / (N + 1)
	if math.Abs(1.0-a) < 1e-3 {
		prDrop = (1 - a) * math.Pow(a, N) / (1 - math.Pow(a, N+1))
	}

	return prDrop, netDelay / (1.0 - a)
}

// passThruDelay returns the time it takes a device (switch or router)
// to perform its operation (switch or route)
func passThruDelay(dev TopoDev, msg any) float64 {
	var model string

	// here hardwired, could extend to be an input argument
	var opType = "switch"

	// to look up the model we need to know whether this is a switch or router
	if dev.DevType() == RouterCode {
		rtrDev := dev.(*routerDev)
		model = rtrDev.RouterModel
	} else {
		switchDev := dev.(*switchDev)
		model = switchDev.SwitchModel
	}

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

// routeTransPerf computes the end-to-end latency for the input route,
// and the minimum bandwidth among all interfaces, devices, and networks
func routeTransitPerf(srcID, dstID int, msg any, route *[]intrfcsToDev, compute bool) (float64, float64) {
	cached, latency, bndwdth := getCachedTransitPerf(srcID, dstID)
	if !compute && cached {
		return latency, bndwdth
	}

	// get the variables declared
	latency = float64(0.0)
	bndwdth = math.MaxFloat64 / 2.0

	// step through every intrfcsToDev entry to get the delays
	// and bandwidths it contributes
	for idx, step := range *route {
		srcIntrfc := IntrfcByID[step.srcIntrfcID]
		dstIntrfc := IntrfcByID[step.dstIntrfcID]
		net := dstIntrfc.Faces

		// include time through interface source
		latency += srcIntrfc.State.Delay
		if srcIntrfc.Cable == nil {
			// FIND N.B. we should compute mean latency here delay is through network (baseline)
			// latency += net.NetState.Latency
			latency += net.NetLatency()
		} else {
			// delay is across a pt-to-pt line
			latency += Pt2ptLatency(srcIntrfc, dstIntrfc)
		}

		// include time through dst interface
		latency += dstIntrfc.State.Delay

		// add in the operation time of the device with dstIntrfc if not the terminus. N.B. assumption
		// here is that this cost is independent of the message
		if idx < len(*route)-1 {
			dev := TopoDevByID[step.devID]
			latency += dev.DevDelay(msg)
		}

		bndwdth = math.Min(bndwdth, srcIntrfc.AvailBndwdth(false))
		bndwdth = math.Min(bndwdth, dstIntrfc.AvailBndwdth(true))
		if srcIntrfc.Carry != nil {
			delta := math.Min(bndwdth, net.AvailBndwdth())
			bndwdth = delta
		}
		if bndwdth < 1.0 {
			bndwdth += 0.0	
		}
	}

	setCachedTransitPerf(srcID, dstID, latency, bndwdth)

	return latency, bndwdth
}
