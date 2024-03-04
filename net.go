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
)

// a rtnRecord saves the event handling function to call when the network simulation
// pushes a message back into the application layer
type rtnRecord struct {
	count   int
	rtnFunc evtm.EventHandlerFunction
	rtnCxt  any
}

type intPair struct {
	i, j int
}

var routeStepIntrfcs map[intPair]intPair

func getRouteStepIntrfcs(srcId, dstId int) (int, int) {
	ip := intPair{i:srcId, j:dstId}
	intrfcs, present := routeStepIntrfcs[ip]
	if !present {
		intrfcs, present = routeStepIntrfcs[intPair{i:dstId, j:srcId}]
		if !present {
			panic(fmt.Errorf("no step between %s and %s\n", topoDevById[srcId].devName(), topoDevById[dstId].devName()))
		}
	}
	return intrfcs.i, intrfcs.j
}


// a NetworkPortal implements the mrnesbits interface used to pass
// traffice between the application layer and the network sim
type NetworkPortal struct {
	QkNetSim bool
	returnTo map[int]*rtnRecord
}

// activePortal remembers the most recent NetworkPortal created
// (there should be only one call to CreateNetworkPortal...)
var activePortal *NetworkPortal

// CreateNetworkPortal is a constructor, passed a flag indicating which
// of two network simulation modes to use, and writes the NetworkPortal pointer into a global variable
func CreateNetworkPortal(qksim bool) *NetworkPortal {
	if activePortal != nil {
		return activePortal
	}

	np := new(NetworkPortal)
	np.QkNetSim = qksim
	np.returnTo = make(map[int]*rtnRecord)
	activePortal = np

	return np
}

// HostCPU helps NetworkPortal implement the mrnesbits NetworkPortal interface,
// returning the CPU type associated with a named host.  Present because the
// application layer does not otherwise have visibility into the network topology
func (np *NetworkPortal) HostCPU(devName string) string {
	host, present := hostDevByName[devName]
	if present {
		return host.hostCPU
	}	
	filter, present := filterDevByName[devName]
	if present {
		return filter.filterCPU
	}
	return ""
}

// Depart is called to return an application message being carried through
// the network back to the application layer
func (np *NetworkPortal) Depart(evtMgr *evtm.EventManager, nm networkMsgEdge) {
	flowId := nm.flowId
	np.returnTo[flowId].count -= 1

	rtnRec := np.returnTo[flowId]
	rtnCxt := rtnRec.rtnCxt
	rtnFunc := rtnRec.rtnFunc

	// schedule the re-integration into the application simulator
	evtMgr.Schedule(rtnCxt, nm.msg, rtnFunc, vrtime.SecondsToTime(0.0))
	if np.returnTo[flowId].count == 0 {
		delete(np.returnTo, flowId)
	}
}

// Arrive is called at the point an application message is received by the network
// and a new flowId is created to track it.  It saves information needed to re-integrate
// the application message into the application layer when the message arrives at its destination
func (np *NetworkPortal) Arrive(flowId int, rtnCxt any, rtnFunc evtm.EventHandlerFunction) {
	rtnRec := &rtnRecord{count: 1, rtnFunc: rtnFunc, rtnCxt: rtnCxt}
	np.returnTo[flowId] = rtnRec
}

// a TraceRecord saves information about the visitation of a message to some point in the simulation.
// saved for post-run analysis
type TraceRecord struct {
	Time     float64 // time in float64
	Ticks    int64   // ticks variable of time
	Priority int64   // priority field of time-stamp
	ExecId   int     // integer identifier identifying the chain of traces this is part of
	FlowId   int     // integer identifier of the network flow
	ObjId    int     // integer id for object being referenced
	ObjType  string  // "host", "switch", "interface", "router"
	ObjEntry bool    // true if the recorded trace is entering an object
	MsgEntry bool    // true if the trace is the leading edge of the message, false otherwise
	Rate     float64 // rate associated with the flow
}

// NameType is a an entry in a dictionary created for a trace
// that maps object id numbers to a (name,type) pair
type NameType struct {
	Name string
	Type string
}

// TraceManger implements the mrnesbits TraceManager interface. It is
// use to gather information about a simulation model and an execution of that model
type TraceManager struct {
	// experiment uses trace
	InUse    bool                  `json:"inuse" yaml:"inuse"`

	// name of experiment
	ExpName  string                `json:"expname" yaml:"expname"`

	// text name associated with each objId
	NameById map[int]NameType      `json:"namebyid" yaml:"namebyid"`

	// all trace records for this experiment
	Traces   map[int][]TraceRecord `json:"traces" yaml:"traces"`
}

// CreateTraceManager is a constructor.  It saves the name of the experiment
// and a flag indicating whether the trace manager is active.  By testing this
// flag we can inhibit the activity of gathering a trace when we don't want it,
// while embedding calls to its methods everywhere we need them when it is
func CreateTraceManager(ExpName string, active bool) *TraceManager {
	tm := new(TraceManager)
	tm.InUse = active
	tm.ExpName = ExpName
	tm.NameById = make(map[int]NameType)    // dictionary of id code -> (name,type)
	tm.Traces = make(map[int][]TraceRecord) // traces have 'execution' origins, are saved by index to these
	return tm
}

// Active tells the caller whether the Trace Manager is activelyl being used
func (tm *TraceManager) Active() bool {
	return tm.InUse
}


// AddTrace creates a record of the trace using its calling arguments, and stores it
func (tm *TraceManager) AddTrace(vrt vrtime.Time, execId, flowId, objId int, objType string,
	objEntry, msgEntry bool, rate float64) {

	// return if we aren't using the trace manager
	if !tm.InUse {
		return
	}

	if execId == 0 {
		fmt.Println("execId is zero")
	}

	_, present := tm.Traces[execId]
	if !present {
		tm.Traces[execId] = make([]TraceRecord, 0)
	}

	// create and add the trace record
	vmr := TraceRecord{Time: vrt.Seconds(), Ticks: vrt.Ticks(), Priority: vrt.Pri(), FlowId: flowId,
		ExecId: execId, ObjType: objType, ObjId: objId, ObjEntry: objEntry, MsgEntry: msgEntry, Rate: rate}

	tm.Traces[execId] = append(tm.Traces[execId], vmr)
}

// AddName is used to add an element to the id -> (name,type) dictionary for the trace file
func (tm *TraceManager) AddName(id int, name string, objType string) {
	if tm.InUse {
		tm.NameById[id] = NameType{Name: name, Type: objType}
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
type devCode int

const (
	hostCode devCode = iota
	switchCode
	routerCode
	filterCode
	unknownCode
)

// devCodefromStr returns the devCode corresponding to an string name for it
func devCodeFromStr(code string) devCode {
	switch code {
	case "Host", "host":
		return hostCode
	case "Switch", "switch":
		return switchCode
	case "Router", "router", "rtr":
		return routerCode
	case "Filter", "filter":
		return filterCode
	default:
		return unknownCode
	}
}

// devCodeToStr returns a string corresponding to an input devCode for it
func devCodeToStr(code devCode) string {
	switch code {
	case hostCode:
		return "Host"
	case switchCode:
		return "Switch"
	case routerCode:
		return "Router"
	case filterCode:
		return "Filter"
	case unknownCode:
		return "Unknown"
	}

	return "Unknown"
}

// networkScale is the base type for an enumerated type of network type descriptions
type networkScale int

const (
	LAN networkScale = iota
	WAN
	T3
	T2
	T1
	GeneralNet
)

// netScaleFromStr returns the networkScale corresponding to an string name for it
func netScaleFromStr(netScale string) networkScale {
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
func netScaleToStr(ntype networkScale) string {
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
type networkMedia int

const (
	wired networkMedia = iota
	wireless
	unknownMedia
)

// netMediaFromStr returns the networkMedia type corresponding to the input string name
func netMediaFromStr(media string) networkMedia {
	switch media {
	case "Wired", "wired":
		return wired
	case "wireless", "Wireless":
		return wireless
	default:
		return unknownMedia
	}
}

var flowId int = 0

func nxtFlowId() int {
	flowId += 1
	return flowId
}

// the topDev interface specifies the functionality different device types provide
type topoDev interface {
	devName() string              // every device has a unique name
	devId() int                   // every device has a unique integer id
	devType() devCode             // every device is one of the devCode types
	devIntrfcs() []*intrfcStruct  // we can get from devices a list of the interfaces they host, if any
	devDelay(any) float64         // every device can be be queried for the delay it introduces for an operation
	devState() any                // every device as a structure of state that can be accessed
	devAddActive(*networkMsgEdge) // add the flowId argument to the device's list of active flows
	devRmActive(int)              // remove the flowId argument to the device's list of active flows
	LogNetEvent(vrtime.Time, int, int, bool, bool, float64)
}

// ParamContainer interface is satisfied by every network object that
// can be configured at run-time with performance parameters. These
// are intrfcStruct, networkStruct, switchDev, hostDev, routerDev
type paramObj interface {
	matchParam(string, string) bool
	setParam(string, valueStruct)
	paramObjName() string
	LogNetEvent(vrtime.Time, int, int, bool, bool, float64)
}

// The intrfcStruct holds information about a network interface embedded in a device
type intrfcStruct struct {
	name     string         // unique name, probably generated automatically
	groups	 []string		// list of groups this interface may belong to
	number   int            // unique integer id, probably generated automatically
	devType  devCode        // device code of the device holding the interface
	media    networkMedia   // media of the network the interface interacts with
	device   topoDev        // pointer to the device holding the interface
	prmDev   paramObj       // pointer to the device holding the interface as a paramObj
	attrib	 string
	carry    *intrfcStruct  // points to the "other" interface in a connection
	cable	 *intrfcStruct  // For a wired interface, points to the "other" interface in the connection
	wireless []*intrfcStruct // For a wired interface, points to the "other" interface in the connection
	faces    *networkStruct // pointer to the network the interface interacts with
	state    *intrfcState   // pointer to the interface's block of state information
}

// The  intrfcState holds parameters descriptive of the interface's capabilities
type intrfcState struct {
	bndwdth    float64         // maximum bandwidth (in Mbytes/sec)
	bufferSize float64         // buffer capacity (in Mbytes)
	latency    float64         // time the leading bit takes to traverse the wire out of the interface
	delay	   float64         // time the leading bit takes to traverse the interface
	mtu		   int             // maximum packet size (bytes)
	trace      bool            // switch for calling add trace
	active     map[int]float64 // id of a flow actively passing through the interface, and its bandwidth
}

func createIntrfcState() *intrfcState {
	iss := new(intrfcState)
	iss.bndwdth = 0.0    // will be initialized or else we'll notice
	iss.bufferSize = 0.0 // not really using bufferSize yet
	iss.delay = 1e+6   // in seconds!  Set this way so that if not initialized we'll notice
	iss.latency = 1e+6
	iss.mtu = 1500  // in bytes Set for Ethernet2 MTU, should change if wireless
	iss.active = make(map[int]float64)
	iss.trace = false
	return iss
}

// createIntrfcStruct is a constructor, building an intrfcStruct from a desc description of the interface
func createIntrfcStruct(intrfc *IntrfcDesc) *intrfcStruct {
	is := new(intrfcStruct)

	is.groups = intrfc.Groups

	// name comes from desc description
	is.name = intrfc.Name

	// unique id is locally generated
	is.number = nxtId()

	// desc representation codes the device type as a string
	switch intrfc.DevType {
	case "Host":
		is.devType = hostCode
	case "Router":
		is.devType = routerCode
	case "Switch":
		is.devType = switchCode
	case "Filter":
		is.devType = filterCode
	}

	// The desc description gives the name of the device hosting the interface.
	// We can use this to look up the locally constructed representation of the device
	// and save a pointer to it
	is.device = topoDevByName[intrfc.Device]
	is.prmDev = paramObjByName[intrfc.Device]

	// desc representation codes the media type as a string
	switch intrfc.MediaType {
	case "wired", "Wired":
		is.media = wired
	case "wireless", "Wireless":
		is.media = wireless
	default:
		is.media = unknownMedia
	}

	is.wireless = make([]*intrfcStruct,0)
	is.state = createIntrfcState()

	return is
}

// matchParam is used to determine whether a run-time parameter description
// should be applied to the interface. Its definition here helps intrfcStruct satisfy
// paramObj interface.  To apply or not to apply depends in part on whether the
// attribute given matchParam as input matches what the interface has. The
// interface attributes that can be tested are the device type of device that hosts it, and the
// media type of the network it interacts with
func (intrfc *intrfcStruct) matchParam(attrbName, attrbValue string) bool {
	switch attrbName {
		case "name":
			return intrfc.name == attrbValue
		case "group":
			return slices.Contains(intrfc.groups, attrbValue)
		case "media":
			return netMediaFromStr(attrbValue) == intrfc.media
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
		intrfc.state.latency = value.floatValue
	case "delay":
		// units of delay are seconds
		intrfc.state.delay = value.floatValue
	case "bandwidth":
		// units of bandwidth are Mbytes/sec
		intrfc.state.bndwdth = value.floatValue
	case "MTU":
		// number of bytes in maximally sized packet
		intrfc.state.mtu = value.intValue
	case "trace":
		intrfc.state.trace = value.boolValue
	}
}

func (intrfc *intrfcStruct) LogNetEvent(time vrtime.Time, execId int, flowId int, objEntry bool, msgEntry bool, rate float64) {
	if !intrfc.state.trace {
		return
	}
	devTraceMgr.AddTrace(time, execId, flowId, intrfc.number, "interface", objEntry, msgEntry, rate)
}

// paramObjName helps intrfcStruct satisfy paramObj interface, returns interface name
func (intrfc *intrfcStruct) paramObjName() string {
	return intrfc.name
}


// linkIntrfcStruct sets the 'connect' and 'faces' values
// of an intrfcStruct based on the names coded in a IntrfcDesc.
func linkIntrfcStruct(intrfcDesc *IntrfcDesc) {
	// look up the intrfcStruct corresponding to the interface named in input intrfc
	is := intrfcByName[intrfcDesc.Name]

	// in IntrfcDesc the 'Cable' field is a string, holding the name of the target interface
	if len(intrfcDesc.Cable) > 0 {
		is.cable = intrfcByName[intrfcDesc.Cable]
	}

	// in IntrfcDesc the 'Cable' field is a string, holding the name of the target interface
	if len(intrfcDesc.Carry) > 0 {
		is.carry = intrfcByName[intrfcDesc.Carry]
	}

	if len(intrfcDesc.Wireless) > 0 {
		for _, intrfcName := range intrfcDesc.Wireless {
			is.wireless = append(is.wireless, intrfcByName[intrfcName])
		}
	}

	// in IntrfcDesc the 'Faces' field is a string, holding the name of the network the interface
	// interacts with
	if len(intrfcDesc.Faces) > 0 {
		is.faces = networkByName[intrfcDesc.Faces]
	}
}

// availBndwdth returns the interface bandwidth available to a new flow
func (intrfc *intrfcStruct) availBndwdth() float64 {
	return intrfc.state.bndwdth
}

// A networkStruct holds the attributes of one of the model's communication subnetworks
type networkStruct struct {
	name          string             // unique name
	groups		  []string			 // list of groups to which network belongs
	number        int                // unique integer id
	netScale      networkScale        // type, e.g., LAN, WAN, etc.
	netMedia      networkMedia       // communication fabric, e.g., wired, wireless
	netRouters    []*routerDev       // list of pointers to routerDevs with interfaces that face this subnetwork
	netSwitches   []*switchDev       // list of pointers to routerDevs with interfaces that face this subnetwork
	netFilters    []*filterDev       // list of pointers to routerDevs with interfaces that face this subnetwork
	netHosts      []*hostDev       // list of pointers to routerDevs with interfaces that face this subnetwork
	netState      *networkState      // pointer to a block of information comprising the network 'state'
}

// A networkState struct holds some static and dynamic information about the network's current state
type networkState struct {
	latency float64         // latency through network (without considering explicitly declared wired connections) under no load
	load    float64         // real-time value of total load (in units of Mbytes/sec)
	bndwdth float64         // maximum bandwidth between any two routers in network
	capacity float64        // maximum traffic capacity of network
	trace   bool            // switch for calling trace saving
	active  map[int]float64 // keep track of flows through the network
}

// initNetworkStruct transforms information from the desc description
// of a network to its networkStruct representation.  This is separated from
// the createNetworkStruct constructor because it requires that the brdcstDmnByName
// and routerDevByName lists have been created, which in turn requires that
// the router constructors have already been called.  So the call to initNetworkStruct
// is delayed until all of the network device constructors have been called.
func (ns *networkStruct) initNetworkStruct(nd *NetworkDesc) {
	// in NetworkDesc a router is referred to through its string name
	ns.netRouters = make([]*routerDev, 0)
	for _, rtrName := range nd.Routers {
		// use the router name from the desc representation to find the run-time pointer
		// to the router and append to the network's list
		ns.addRouter(routerDevByName[rtrName])
	}
	
	ns.netHosts = make([]*hostDev, 0)
	for _, hostName := range nd.Hosts {
		// use the router name from the desc representation to find the run-time pointer
		// to the router and append to the network's list
		ns.addHost(hostDevByName[hostName])
	}

	ns.netFilters = make([]*filterDev, 0)
	for _, filterName := range nd.Filters {
		// use the router name from the desc representation to find the run-time pointer
		// to the router and append to the network's list
		ns.addFilter(filterDevByName[filterName])
	}

	ns.netSwitches = make([]*switchDev, 0)
	for _, switchName := range nd.Switches {
		// use the router name from the desc representation to find the run-time pointer
		// to the router and append to the network's list
		ns.addSwitch(switchDevByName[switchName])
	}
	ns.groups = nd.Groups
}

// createNetworkStruct is a constructor that initialized some of the features of the networkStruct
// from their expression in a desc representation of the network
func createNetworkStruct(nd *NetworkDesc) *networkStruct {
	ns := new(networkStruct)

	// copy the name
	ns.name = nd.Name

	ns.groups = []string{}

	// get a unique integer id locally
	ns.number = nxtId()

	// get a netScale type from a desc string expression of it
	ns.netScale = netScaleFromStr(nd.NetScale)

	// get a netMedia type from a desc string expression of it
	ns.netMedia = netMediaFromStr(nd.MediaType)

	// initialize the Router lists, to be filled in by
	// initNetworkStruct after the router constructors are called
	ns.netRouters = make([]*routerDev, 0)
	ns.netHosts  = make([]*hostDev, 0)
	ns.netFilters = make([]*filterDev, 0)
	
	// make the state structure, will flesh it out from run-time configuration parameters
	ns.netState = new(networkState)
	ns.netState.active = make(map[int]float64)

	return ns
}

// matchParam is used to determine whether a run-time parameter description
// should be applied to the network. Its definition here helps networkStruct satisfy
// paramObj interface.  To apply or not to apply depends in part on whether the
// attribute given matchParam as input matches what the interface has. The
// interface attributes that can be tested are the media type, and the nework type
func (ns *networkStruct) matchParam(attrbName, attrbValue string) bool {
	switch attrbName {
		case "name":
			return ns.name == attrbValue
		case "group":
			return slices.Contains(ns.groups, attrbValue)
		case "media":
			return netMediaFromStr(attrbValue) == ns.netMedia
		case "scale":
			return ns.netScale == netScaleFromStr(attrbValue)
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
			ns.netState.latency = fltValue
		case "bandwidth": 
			ns.netState.bndwdth = fltValue
		case "capacity": 
			ns.netState.capacity = fltValue
		case "trace":
			ns.netState.trace = value.boolValue
		}
}

// paramObjName helps networkStruct satisfy paramObj interface, returns network name
func (ns *networkStruct) paramObjName() string {
	return ns.name
}

func (ns *networkStruct) LogNetEvent(time vrtime.Time, execId int, flowId int,
	objEntry, msgEntry bool, rate float64) {
	if !ns.netState.trace {
		return
	}
	devTraceMgr.AddTrace(time, execId, flowId, ns.number, "network", objEntry, msgEntry, rate)
}

// addRouter includes the router given as input parameter on the network list of routers that face it
func (ns *networkStruct) addRouter(newrtr *routerDev) {
	// skip if rtr already exists in network netRouters list
	for _, rtr := range ns.netRouters {
		if rtr == newrtr || rtr.routerName == newrtr.routerName {
			return
		}
	}
	ns.netRouters = append(ns.netRouters, newrtr)
}

// addHost includes the host given as input parameter on the network list of hosts that face it
func (ns *networkStruct) addHost(newhost *hostDev) {
	// skip if host already exists in network netHosts list
	for _, host := range ns.netHosts {
		if host == newhost || host.hostName == newhost.hostName {
			return
		}
	}
	ns.netHosts = append(ns.netHosts, newhost)
}

// addFilter includes the filter given as input parameter on the network list of filters that face it
func (ns *networkStruct) addFilter(newfilter *filterDev) {
	// skip if filter already exists in network netFilters list
	for _, filter := range ns.netFilters {
		if filter == newfilter || filter.filterName == newfilter.filterName {
			return
		}
	}
	ns.netFilters = append(ns.netFilters, newfilter)
}

// addSwitch includes the swtch given as input parameter on the network list of swtchs that face it
func (ns *networkStruct) addSwitch(newswtch *switchDev) {
	// skip if swtch already exists in network netSwitches list
	for _, swtch := range ns.netSwitches {
		if swtch == newswtch || swtch.switchName == newswtch.switchName {
			return
		}
	}
	ns.netSwitches = append(ns.netSwitches, newswtch)
}

// netBndwdth returns the current bandwidth available for a new flow
func (ns *networkStruct) availBndwdth() float64 {
	return ns.netState.bndwdth
}

// a filterDev holds information about a filter
type filterDev struct {
	filterName      string          // unique name
	filterModel     string          // type of CPU the filter uses
	filterCPU       string          // type of CPU the filter uses
	filterGroups	[]string        // list of groups to which filter belongs
	filterId        int             // unique integer id
	filterIntrfcs   []*intrfcStruct // list of network interfaces embedded in the filter
	filterState     *filterDevState   // a struct holding filter state
}

// a filterDevState holds extra informat used by the filter
type filterDevState struct {
	rngstrm *rngstream.RngStream // pointer to a random number generator
	filterAccelerator bool		 // whether there is a crypto accelerator on-board
	trace   bool                 // switch for calling add trace
	active  map[int]float64
}

// matchParam is for other paramObj objects a method for seeing whether
// the device attribute matches the input.  'cept the filterDev is not declared
// to have any such attributes, so this function (included to let filterDev be
// a paramObj) returns false.  Included to allow filterDev to satisfy paramObj interface requirements
func (filter *filterDev) matchParam(attrbName, attrbValue string) bool {
	switch attrbName {
		case "name":
			return filter.filterName == attrbValue
		case "group":
			return slices.Contains(filter.filterGroups, attrbValue)
		case "CPU":
			return filter.filterCPU == attrbValue
		case "model":
			return filter.filterModel == attrbValue
	}

	// an error really, as we should match only the names given in the switch statement above
	return false
}

// setParam gives a value to a filterDev parameter.  The design allows only
// the CPU parameter to be set, which is allowed here
func (filter *filterDev) setParam(param string, value valueStruct) {
	switch param {
		case "CPU":
			filter.filterCPU = value.stringValue
		case "trace":
			filter.filterState.trace = value.boolValue
		case "model":
			filter.filterModel = value.stringValue
		case "accelerator":
			filter.filterState.filterAccelerator = value.boolValue
	}
}

// paramObjName helps filterDev satisfy paramObj interface, returns the filter's name
func (filter *filterDev) paramObjName() string {
	return filter.filterName
}

// createFilterDev is a constructor, using information from the desc description of the filter
func createFilterDev(filterDesc *FilterDesc) *filterDev {
	filter := new(filterDev)
	filter.filterName = filterDesc.Name             // unique name
	filter.filterModel = filterDesc.Model
	filter.filterCPU = filterDesc.CPU
	filter.filterId = nxtId()                       // unique integer id, generated at model load-time
	filter.filterIntrfcs = make([]*intrfcStruct, 0) // initialization of list of interfaces, to be augmented later
	filter.filterGroups = filterDesc.Groups
	filter.filterState = new(filterDevState)          // creation of state block, to be augmented later
	filter.filterState.active = make(map[int]float64)
	filter.filterState.trace = false
	return filter
}

// addIntrfc appends the input intrfcStruct to the list of interfaces embedded in the filter.
func (filter *filterDev) addIntrfc(intrfc *intrfcStruct) {
	filter.filterIntrfcs = append(filter.filterIntrfcs, intrfc)
}

// rng returns a pointer to the random number stream used by all functions on the filter
func (filter *filterDev) rng() *rngstream.RngStream {
	fds := filter.filterState
	return fds.rngstrm
}

// rng resturns the string type description of the CPU running the filter
func (filter *filterDev) CPU() string {
	return filter.filterCPU
}

// devName returns the filter name, as part of the topoDev interface
func (filter *filterDev) devName() string {
	return filter.filterName
}

// devId returns the filter integer id, as part of the topoDev interface
func (filter *filterDev) devId() int {
	return filter.filterId
}

// devType returns the filter's device type, as part of the topoDev interface
func (filter *filterDev) devType() devCode {
	return filterCode
}

// devIntrfcs returns the filter's list of interfaces, as part of the topoDev interface
func (filter *filterDev) devIntrfcs() []*intrfcStruct {
	return filter.filterIntrfcs
}

// devState returns the filter's state struct, as part of the topoDev interface
func (filter *filterDev) devState() any {
	return filter.filterState
}

func (filter *filterDev) LogNetEvent(time vrtime.Time, execId int, flowId int, objEntry bool, msgEntry bool, rate float64) {
	if !filter.filterState.trace {
		return
	}
	devTraceMgr.AddTrace(time, execId, flowId, filter.filterId, "filter", objEntry, msgEntry, rate)
}

// devAddActive adds an active flow, as part of the topoDev interface.  Not used for filters, yet
func (filter *filterDev) devAddActive(nme *networkMsgEdge) {
	filter.filterState.active[nme.flowId] = nme.rate
}

// devRmActive removes an active flow, as part of the topoDev interface.  Not used for filters, yet
func (filter *filterDev) devRmActive(flowId int) {
	delete(filter.filterState.active, flowId)
}

// devDelay returns the state-dependent delay for passage through the device, as part of the topoDev interface.
// Not really applicable to filter, so zero is returned
func (filter *filterDev) devDelay(arg any) float64 {
	return 0.0
}

// a hostDev holds information about a host
type hostDev struct {
	hostName      string          // unique name
	hostGroups	  []string		  // list of groups to which host belongs
	hostCPU		  string          // type of CPU the host uses
	hostModel     string          // unique name
	hostId        int             // unique integer id
	hostIntrfcs   []*intrfcStruct // list of network interfaces embedded in the host
	hostState     *hostDevState   // a struct holding host state
}

// a hostDevState holds extra informat used by the host
type hostDevState struct {
	rngstrm *rngstream.RngStream // pointer to a random number generator
	trace   bool                 // switch for calling add trace
	active  map[int]float64
}

// matchParam is for other paramObj objects a method for seeing whether
// the device attribute matches the input.  'cept the hostDev is not declared
// to have any such attributes, so this function (included to let hostDev be
// a paramObj) returns false.  Included to allow hostDev to satisfy paramObj interface requirements
func (host *hostDev) matchParam(attrbName, attrbValue string) bool {
	switch attrbName {
		case "name":
			return host.hostName == attrbValue
		case "group":
			return slices.Contains(host.hostGroups, attrbValue)
		case "model":
			return host.hostModel == attrbValue
		case "CPU":
			return host.hostCPU == attrbValue
	}

	// an error really, as we should match only the names given in the switch statement above
	return false
}

// setParam gives a value to a hostDev parameter.  The design allows only
// the CPU parameter to be set, which is allowed here
func (host *hostDev) setParam(param string, value valueStruct) {
	switch param {
		case "CPU":
			host.hostCPU = value.stringValue
		case "trace":
			host.hostState.trace = value.boolValue
		case "model":
			host.hostModel = value.stringValue
	}
}

// paramObjName helps hostDev satisfy paramObj interface, returns the host's name
func (host *hostDev) paramObjName() string {
	return host.hostName
}

// createHostDev is a constructor, using information from the desc description of the host
func createHostDev(hostDesc *HostDesc) *hostDev {
	host := new(hostDev)
	host.hostName = hostDesc.Name               // unique name
	host.hostModel = hostDesc.Model             
	host.hostCPU = hostDesc.CPU			
	host.hostId = nxtId()                       // unique integer id, generated at model load-time
	host.hostIntrfcs = make([]*intrfcStruct, 0) // initialization of list of interfaces, to be augmented later
	host.hostGroups = hostDesc.Groups
	host.hostState = new(hostDevState)          // creation of state block, to be augmented later
	host.hostState.active = make(map[int]float64)
	host.hostState.trace = false
	return host
}

// addIntrfc appends the input intrfcStruct to the list of interfaces embedded in the host.
func (host *hostDev) addIntrfc(intrfc *intrfcStruct) {
	host.hostIntrfcs = append(host.hostIntrfcs, intrfc)
}

// rng returns a pointer to the random number stream used by all functions on the host
func (host *hostDev) rng() *rngstream.RngStream {
	hds := host.hostState
	return hds.rngstrm
}

// rng resturns the string type description of the CPU running the host
func (host *hostDev) CPU() string {
	return host.hostCPU
}

// devName returns the host name, as part of the topoDev interface
func (host *hostDev) devName() string {
	return host.hostName
}

// devId returns the host integer id, as part of the topoDev interface
func (host *hostDev) devId() int {
	return host.hostId
}

// devType returns the host's device type, as part of the topoDev interface
func (host *hostDev) devType() devCode {
	return hostCode
}

// devIntrfcs returns the host's list of interfaces, as part of the topoDev interface
func (host *hostDev) devIntrfcs() []*intrfcStruct {
	return host.hostIntrfcs
}

// devState returns the host's state struct, as part of the topoDev interface
func (host *hostDev) devState() any {
	return host.hostState
}

func (host *hostDev) LogNetEvent(time vrtime.Time, execId int, flowId int, objEntry bool, msgEntry bool, rate float64) {
	if !host.hostState.trace {
		return
	}
	devTraceMgr.AddTrace(time, execId, flowId, host.hostId, "host", objEntry, msgEntry, rate)
}

// devAddActive adds an active flow, as part of the topoDev interface.  Not used for hosts, yet
func (host *hostDev) devAddActive(nme *networkMsgEdge) {
	host.hostState.active[nme.flowId] = nme.rate
}

// devRmActive removes an active flow, as part of the topoDev interface.  Not used for hosts, yet
func (host *hostDev) devRmActive(flowId int) {
	delete(host.hostState.active, flowId)
}

// devDelay returns the state-dependent delay for passage through the device, as part of the topoDev interface.
// Not really applicable to host, so zero is returned
func (host *hostDev) devDelay(arg any) float64 {
	return 0.0
}

// The switchDev struct holds information describing a run-time representation of a switch
type switchDev struct {
	switchName    string          // unique name
	switchGroups  []string		  // groups to which the switch may belong
	switchModel   string          // model name, used to identify performance characteristics
	switchId      int             // unique integer id, generated at model-load time
	switchIntrfcs []*intrfcStruct // list of network interfaces embedded in the switch
	switchState   *switchDevState // pointer to the switch's state struct
}

// The switchDevState struct holds auxiliary information about the switch
type switchDevState struct {
	execTime float64 // nominal time for a packet to pass through the switch, in seconds
	buffer   float64 // buffer capacity within switch, in Mbytes
	trace    bool    // switch for calling trace saving
	active   map[int]float64
}

// createSwitchDev is a constructor, initializing a run-time representation of a switch from its desc description
func createSwitchDev(switchDesc *SwitchDesc) *switchDev {
	swtch := new(switchDev)
	swtch.switchName = switchDesc.Name
	swtch.switchModel = switchDesc.Model
	swtch.switchId = nxtId()
	swtch.switchIntrfcs = make([]*intrfcStruct, 0)
	swtch.switchGroups = switchDesc.Groups
	swtch.switchState = new(switchDevState)
	swtch.switchState.active = make(map[int]float64)
	swtch.switchState.trace = false
	return swtch
}

// matchParam is used to determine whether a run-time parameter description
// should be applied to the switch. Its definition here helps switchDev satisfy
// the paramObj interface.  To apply or not to apply depends in part on whether the
// attribute given matchParam as input matches what the switch has. 'model' is the
// only attribute we use to match a switch
func (swtch *switchDev) matchParam(attrbName, attrbValue string) bool {
	switch attrbName {
		case "name":
			return swtch.switchName == attrbValue
		case "group":
			return slices.Contains(swtch.switchGroups, attrbValue)
		case "model":
			return swtch.switchModel == attrbValue
	}

	// an error really, as we should match only the names given in the switch statement above
	return false
}

// setParam gives a value to a switchDev parameter, to help satisfy the paramObj interface.
// Parameters that can be altered on a switch are "model", "execTime", and "buffer"
func (swtch *switchDev) setParam(param string, value valueStruct) {
	switch param {
	case "model":
		swtch.switchModel = value.stringValue
	case "buffer":
		swtch.switchState.buffer = value.floatValue
	case "trace":
		swtch.switchState.trace = value.boolValue
	}
}

// paramObjName returns the switch name, to help satisfy the paramObj interface.
func (swtch *switchDev) paramObjName() string {
	return swtch.switchName
}

// addIntrfc appends the input intrfcStruct to the list of interfaces embedded in the switch.
func (swtch *switchDev) addIntrfc(intrfc *intrfcStruct) {
	swtch.switchIntrfcs = append(swtch.switchIntrfcs, intrfc)
}

// devName returns the switch name, as part of the topoDev interface
func (swtch *switchDev) devName() string {
	return swtch.switchName
}

// devId returns the switch integer id, as part of the topoDev interface
func (swtch *switchDev) devId() int {
	return swtch.switchId
}

// devType returns the switch's device type, as part of the topoDev interface
func (swtch *switchDev) devType() devCode {
	return switchCode
}

// devIntrfcs returns the switch's list of interfaces, as part of the topoDev interface
func (swtch *switchDev) devIntrfcs() []*intrfcStruct {
	return swtch.switchIntrfcs
}

// devState returns the switch's state struct, as part of the topoDev interface
func (swtch *switchDev) devState() any {
	return swtch.switchState
}

// devAddActive adds a flow id to the list of active flows through the switch, as part of the topoDev interface
func (swtch *switchDev) devAddActive(nme *networkMsgEdge) {
	swtch.switchState.active[nme.flowId] = nme.rate
}

// devRmActive removes a flow id to the list of active flows through the switch, as part of the topoDev interface
func (swtch *switchDev) devRmActive(flowId int) {
	delete(swtch.switchState.active, flowId)
}

// devDelay returns the state-dependend delay for passage through the switch, as part of the topoDev interface.
func (swtch *switchDev) devDelay(msg any) float64 {
	nm := msg.(networkMsgEdge)
	delay := passThruDelay(swtch, nm.msg)
	// N.B. we could put load-dependent scaling factor here
	return delay
}

// LogNetEvent satisfies topoDev interface
func (swtch *switchDev) LogNetEvent(time vrtime.Time, execId int, flowId int, objEntry bool, msgEntry bool, rate float64) {
	if !swtch.switchState.trace {
		return
	}
	devTraceMgr.AddTrace(time, execId, flowId, swtch.switchId, "switch", objEntry, msgEntry, rate)
}

// The routerDev struct holds information describing a run-time representation of a router
type routerDev struct {
	routerName      string          // unique name
	routerGroups	[]string		// list of groups to which the router belongs
	routerModel     string          // attribute used to identify router performance characteristics
	routerId        int             // unique integer id assigned at model-load time
	routerIntrfcs   []*intrfcStruct // list of interfaces embedded in the router
	routerState     *routerState    // pointer to the struct of the routers auxiliary state
}

// The routerState type describes auxiliary information about the router
type routerState struct {
	buffer   float64 // volume of traffic that can be buffered, expressed in Mbytes
	execTime float64 // nominal time for a packet to transit the router
	trace    bool    // switch for calling trace saving
	active   map[int]float64
}

// createRouterDev is a constructor, initializing a run-time representation of a router from its desc representation
func createRouterDev(routerDesc *RouterDesc) *routerDev {
	router := new(routerDev)
	router.routerName = routerDesc.Name
	router.routerModel = routerDesc.Model
	router.routerId = nxtId()
	router.routerIntrfcs = make([]*intrfcStruct, 0)
	router.routerGroups = routerDesc.Groups
	router.routerState = new(routerState)
	router.routerState.active = make(map[int]float64)
	router.routerState.trace = false
	return router
}

// matchParam is used to determine whether a run-time parameter description
// should be applied to the router. Its definition here helps switchDev satisfy
// the paramObj interface.  To apply or not to apply depends in part on whether the
// attribute given matchParam as input matches what the router has. 'model' is the
// only attribute we use to match a router
func (router *routerDev) matchParam(attrbName, attrbValue string) bool {
	switch attrbName {
		case "name":
			return router.routerName == attrbValue
		case "group":
			return slices.Contains(router.routerGroups, attrbValue)
		case "model":
			return router.routerModel == attrbValue
	}

	// an error really, as we should match only the names given in the switch statement above
	return false
}

// setParam gives a value to a routerDev parameter, to help satisfy the paramObj interface.
// Parameters that can be altered on a router are "model", "execTime", and "buffer"
func (router *routerDev) setParam(param string, value valueStruct) {
	switch param {
		case "model":
			router.routerModel = value.stringValue
		case "buffer":
			router.routerState.buffer = value.floatValue
		case "trace":
			router.routerState.trace = value.boolValue
	}
}

// paramObjName returns the router name, to help satisfy the paramObj interface.
func (router *routerDev) paramObjName() string {
	return router.routerName
}

// addIntrfc appends the input intrfcStruct to the list of interfaces embedded in the router.
func (router *routerDev) addIntrfc(intrfc *intrfcStruct) {
	router.routerIntrfcs = append(router.routerIntrfcs, intrfc)
}

// devName returns the router name, as part of the topoDev interface
func (router *routerDev) devName() string {
	return router.routerName
}

// devId returns the switch integer id, as part of the topoDev interface
func (router *routerDev) devId() int {
	return router.routerId
}

// devType returns the router's device type, as part of the topoDev interface
func (router *routerDev) devType() devCode {
	return routerCode
}

// devIntrfcs returns the routers's list of interfaces, as part of the topoDev interface
func (router *routerDev) devIntrfcs() []*intrfcStruct {
	return router.routerIntrfcs
}

// devState returns the routers's state struct, as part of the topoDev interface
func (router *routerDev) devState() any {
	return router.routerState
}

func (rtr *routerDev) LogNetEvent(time vrtime.Time, execId int, flowId int, objEntry bool, msgEntry bool, rate float64) {
	if !rtr.routerState.trace {
		return
	}
	devTraceMgr.AddTrace(time, execId, flowId, rtr.routerId, "router", objEntry, msgEntry, rate)
}

// devAddActive includes a flowId as part of what is active at the device, as part of the topoDev interface
func (router *routerDev) devAddActive(nme *networkMsgEdge) {
	router.routerState.active[nme.flowId] = nme.rate
}

// devRmActive removes a flowId as part of what is active at the device, as part of the topoDev interface
func (router *routerDev) devRmActive(flowId int) {
	delete(router.routerState.active, flowId)
}

// devDelay returns the state-dependent delay for passage through the router, as part of the topoDev interface.
func (router *routerDev) devDelay(msg any) float64 {
	delay := passThruDelay(router, msg)
	// N.B. we could put load-dependent scaling factor here

	return delay
}

// The intrfcsToDev struct describes a connection to a device.  Used in route descriptions
type intrfcsToDev struct {
	srcIntrfcId int // id of the interface where the connection starts
	dstIntrfcId int // id of the interface embedded by the target device
	netId       int // id of the network between the src and dst interfaces
	devId       int // id of the device the connection targets
}

// The networkMsgEdge type creates a wrapper for a message between comp pattern funcs.
// One value (stepIdx) indexes into a list of route steps, so that by incrementing
// we can find 'the next' step in the route.  One value is a pointer to this route list,
// and the final value is a pointe to an inter-func comp pattern message.  The "Edge"
// piece comes from associating the struct with a bit in the message, typically
// the first or last bit.  The message is treated like a flow that starts and stops
type networkMsgEdge struct {
	stepIdx int             // position within the route from source to destination
	route   *[]intrfcsToDev // pointer to description of route
	flowId  int             // flow identifier
	execId  int             // execution id given by app at entry
	rate    float64         // effective bandwidth for message
	bit     int             // bit number
	msgLen  int             // length of the entire message, in Mbytes
	end     bool            // bit 0 starts the message, 'end' flagged true means it is the last one.
	msg     any             // message being carried.
}

// pt2ptLatency computes the latency on a point-to-point connection
// between interfaces.  Called when neither interface is attached to a router
func pt2ptLatency(srcIntrfc, dstIntrfc *intrfcStruct) float64 {
	return math.Max(srcIntrfc.state.latency, dstIntrfc.state.latency)
}

// currentIntrfcs returns pointers to the source and destination interfaces whose
// id values are carried in the current step of the route followed by the input argument networkMsgEdge.
// If the interfaces are not directly connected but communicate through a network,
// a pointer to that network is returned also
func currentIntrfcs(nm *networkMsgEdge) (*intrfcStruct, *intrfcStruct, *networkStruct) {
	srcIntrfcId := (*nm.route)[nm.stepIdx].srcIntrfcId
	dstIntrfcId := (*nm.route)[nm.stepIdx].dstIntrfcId

	srcIntrfc := intrfcById[srcIntrfcId]
	dstIntrfc := intrfcById[dstIntrfcId]

	var ns *networkStruct = nil
	netId := (*nm.route)[nm.stepIdx].netId
	if netId != -1 {
		ns = networkById[netId]
	} else {
		ns = networkById[commonNetId(srcIntrfc, dstIntrfc)]
	}

	return srcIntrfc, dstIntrfc, ns
}

// transitDelay returns the length of time (in seconds) taken
// by the input argument networkMsgEdge to traverse the current step in the route it follows.
// That step may be a point-to-point wired connection, or may be transition through a network
// w/o specification of passage through specific devices. In addition a pointer to the
// network transited (if any) is returned
func transitDelay(nm *networkMsgEdge) (float64, *networkStruct) {
	var delay float64

	// recover the interfaces themselves and the network between them, if any
	srcIntrfc, dstIntrfc, net := currentIntrfcs(nm)

	if (srcIntrfc.cable != nil && dstIntrfc.cable == nil) ||
		 (srcIntrfc.cable == nil && dstIntrfc.cable != nil) {
			panic("cabled interface confusion")
	}

	if srcIntrfc.cable == nil {
		// delay is through network (baseline) 
		delay = net.netState.latency
	} else {
		// delay is across a pt-to-pt line
		delay = pt2ptLatency(srcIntrfc, dstIntrfc)
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
// pass through an interface instaneously, they 'flow' across links, through networks, and through devices.
// Flows have bandwidth, and every interface, link, device, and network has its own bandwidth
// limit.  From the point of view of the receiving host, the effective bandwidth (assuming the bandwidths
// all along the path don't change) is the minimum bandwidth among all the things on the path.
// In the path before the hop that induces the least bandwidth message may scoot through connections
// at higher bandwidth, but the progress of that flow is ultimately limited  by the smallest bandwidth.
// The simplifying assumption then is that the flow of the message's bits _everywhere_ along the path
// is at that minimum bandwidth.   More detail in tracking and changing bandwidths along sections of the message
// are possible, but at this point it is more complicated and time-consuming to do that than it seems to be worth
// for anticipated use-cases.
//
// The third assumption is that for the life of a packet traversing from source host to destination host,
// the latencies across interfaces, wires, devices, and networks are constant.  This just means they
// don't change while the message is in motion, which means we can compute what the end-to-end latency will be.
// Latencies can be permitted to change in a state-dependent way, so the assumption is time-limited.
//
// With this understanding, the mrnsbit network simulator accepts a message carried in a cmpPtnMsg and
// creates two instances of the 'networkMsgEdge' type. One represents the first bit of the message, the
// second the last bit.  The wrapper for the leading bit is inserted into the network, and the wrapper
// for the trailing bit is inserted at a time later equal to the time it would take the message to completely pass
// a point at the minimum rate described earlier.   Then the two networkMsgEdge structs pass through
// every interface, link, network, and device, one following the other at a fixed delay in time, until
// the trailing edge struct is completely captured by the destination host.  Here (and then) the cmpPtnMsg
// is extracted and presented to the CompPattern Func scheduling apperatus.

// EnterNetwork is called after the execution from the application layer
// It creates networkMsgEdge structs to represent the start and end of the message, and
// schedules their arrival to the egress interface of the message source host
// func enterNetwork(evtMgr *evtm.EventManager, cpf cmpPtnFunc, cpm *cmpPtnMsg) any {
func (np *NetworkPortal) EnterNetwork(evtMgr *evtm.EventManager, srcDev, dstDev string, msgLen int,
	execId int, rate float64, msg any, rtnCxt any, rtnFunc evtm.EventHandlerFunction) any {

	// srcId := hostDevByName[srcHost].hostId
	// dstId := hostDevByName[dstHost].hostId
	srcId := topoDevByName[srcDev].devId()
	dstId := topoDevByName[dstDev].devId()

	// get the route from srcId to dstId
	route := findRoute(srcId, dstId)

	if route == nil || len(*route) == 0 {
		panic(fmt.Errorf("unable to find a route %s -> %s\n", srcDev, dstDev))
	}
		
	// get the latency and bandwidth for traffic on this path, computed
	// 'now'
	latency, bndwdth := routeTransitPerf(route)

	// if the incoming rate is greater than 0, include it in the path minimum
	if rate > 0.0 {
		bndwdth = math.Min(bndwdth, rate)
	}

	// starting a new flow, albeit short-lived!  Record what to do when
	// this flow exits
	//
	flowId := nxtFlowId()

	// if the command line selected the -qnetsim option we skip the
	// detailed network simulation and jump straight to the receipt
	// of the last bit on exit of the interface at the host where the
	// message is routed
	if activePortal.QkNetSim {
		// covert the number of bytes pushing pushed into the number of Mbytes being pushed,
		// work out how long it takes for that many bytes to transit the pipe after the first bit,
		// and add to the network latency to determine when the last bit emerges
		delay := latency + (float64(msgLen)/1e6)/bndwdth

		// data structure that describes the message, noting that the 'edge' is the last bit
		trailingEdge := networkMsgEdge{stepIdx: len(*route) - 1, route: route, rate: bndwdth, msgLen: msgLen, bit: msgLen*8 - 1,
			end: true, flowId: flowId, execId: execId, msg: msg}

		// The destination interface of the last routing step is where the message ultimately emerges from the network.
		// That's the 'context' for the event-handler which deals with this data structure just as though it came off
		// the detailed network simulation
		ingressIntrfcId := (*route)[len(*route)-1].dstIntrfcId
		ingressIntrfc := intrfcById[ingressIntrfcId]

		// schedule exit from final interface after msg passes through
		evtMgr.Schedule(ingressIntrfc, trailingEdge, exitIngressIntrfc, vrtime.SecondsToTime(delay))

		return flowId
	}

	np.Arrive(flowId, rtnCxt, rtnFunc)

	// No quick network simulation, so make a message wrapper and push the message at the entry
	// of the host's egress interface

	nm := networkMsgEdge{stepIdx: 0, route: route, rate: bndwdth, end: false, bit: 0, msgLen: msgLen,
		flowId: flowId, execId: execId, msg: msg}

	// get identity of egress interface
	intrfc := intrfcById[(*route)[0].srcIntrfcId]
	intrfc.faces.LogNetEvent(evtMgr.CurrentTime(), nm.execId, nm.flowId, true, !nm.end, bndwdth)

	// how long to get through the device to the interface?
	delay := (float64(msgLen*8) / 1e6) / bndwdth

	// schedule the entry of the leading networkMsgEdge into the first egress interface (from the source host)
	// once the whole packet has flowed through
	evtMgr.Schedule(intrfc, nm, enterEgressIntrfc, vrtime.SecondsToTime(delay))

	return flowId
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
	nm := msg.(networkMsgEdge)
	nm.end = false

	// remove the flow from the device	
	thisDev := intrfc.device
	thisDev.devRmActive(nm.flowId)

	// mark that this flow is passing through the interface
	intrfc.state.active[nm.flowId] = nm.rate
	intrfc.LogNetEvent(evtMgr.CurrentTime(), nm.execId, nm.flowId, true, !nm.end, nm.rate)
	thisDev.LogNetEvent(evtMgr.CurrentTime(), nm.execId, nm.flowId, false, !nm.end, nm.rate)
	
	// get delay through interface and schedule the leading edge arrival at exitEgressIntrfc
	delay := intrfc.state.delay
	nm.end = false
	evtMgr.Schedule(egressIntrfc, nm, exitEgressIntrfc, vrtime.SecondsToTime(delay))

	// make a copy of the networkMsgEdge and set the end bit
	newEdge := nm
	newEdge.end = true

	// the trailing edge arrives after a delay comprised of first bit delay, and interface bndwdth
	passage := float64(nm.msgLen*8)/(1e+6*intrfc.availBndwdth())

	// schedule exit from this interface after msg passes through. 
	// Note that entire message delivered to exitEgressIntrfc
	evtMgr.Schedule(egressIntrfc, newEdge, exitEgressIntrfc, vrtime.SecondsToTime(delay+passage))

	// event-handlers are required to return _something_
	return nil
}

// exitEgressIntrfc implements an event handler for the departure of a message from an interface.
// It determines the time-through-network of the message edge and schedules the arrival
// of the edge at the ingress interface
func exitEgressIntrfc(evtMgr *evtm.EventManager, egressIntrfc any, msg any) any {
	intrfc := egressIntrfc.(*intrfcStruct)
	nm := msg.(networkMsgEdge)

	intrfc.LogNetEvent(evtMgr.CurrentTime(), nm.execId, nm.flowId, false, !nm.end, nm.rate)

	// transitDelay will differentiate between point-to-point wired connection and passage through a network
	netDelay, net := transitDelay(&nm)

	rate := net.availBndwdth()
	if nm.rate > 0.0 {
		rate = math.Min(rate, nm.rate)
	}

	// mark the rate at which the message is traveling
	nm.rate = rate

	// if this is entering a network, mark it
	if !nm.end {
		net.netState.active[nm.flowId] = nm.rate
		net.netState.load += nm.rate
	} else {
		// remove flow from those active on the interface
		_, present := intrfc.state.active[nm.flowId]
		if present {
			delete(intrfc.state.active, nm.flowId)
		}
		
		// add to netDelay the bandwidth and message-length-dependent time of transfer.
		netDelay += float64(nm.msgLen*8)/(rate*1e+6)
	}


	// schedule arrival of the networkMsgEdge at the next interface
	nxtIntrfc := intrfcById[(*nm.route)[nm.stepIdx].dstIntrfcId]
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
	nm := msg.(networkMsgEdge)

	intrfc.LogNetEvent(evtMgr.CurrentTime(), nm.execId, nm.flowId, true, !nm.end, nm.rate)

	// if the first bit, mark that flow is occupying interface
	intrfc.state.active[nm.flowId] = nm.rate

	// if this is not the trailing edge, wait until it shows up
	if !nm.end {
		return nil
	}

	// reduce load on network just left 
	if intrfc.faces != nil {
		rate, present := intrfc.faces.netState.active[nm.flowId]
		if present {
			intrfc.faces.netState.load -= rate
			delete(intrfc.faces.netState.active, nm.flowId)
		}
	}

	// get delay through interface
	delay := intrfc.state.delay

	// the msg is all together, remark 'end' to be false
	nm.end = false

	// schedule exit from this interface after msg passes through
	evtMgr.Schedule(ingressIntrfc, nm, exitIngressIntrfc, vrtime.SecondsToTime(delay))

	// event handlers are required to return _something_
	return nil
}

// exitIngressIntrfc is the event handler for the arrival of a message at an interface facing the connection
// through which the networkMsgEdge arrived. When this event handler is called the entire 
// msg is exiting. If this device is a host or filter then accept the message 
// and push it into the CompPattern Func scheduling system. Otherwise compute the time the edge hits
// the egress interface on the other side of device and schedule that arrival
func exitIngressIntrfc(evtMgr *evtm.EventManager, ingressIntrfc any, msg any) any {
	intrfc := ingressIntrfc.(*intrfcStruct)
	nm := msg.(networkMsgEdge)

	// log passage of msg through the interface
	intrfc.LogNetEvent(evtMgr.CurrentTime(), nm.execId, nm.flowId, false, !nm.end, nm.rate)

	// take the flow off the active list
	delete(intrfc.state.active, nm.flowId)

	// log entry of packet into device 	
	intrfc.prmDev.LogNetEvent(evtMgr.CurrentTime(), nm.execId, nm.flowId, true, !nm.end, nm.rate)

	// check whether the device is a host, in which case leave the network if this is the last bit
	devCode := intrfc.device.devType()
	if  devCode == hostCode || devCode == filterCode {
		// schedule return into comp pattern system, where requested
		intrfc.device.LogNetEvent(evtMgr.CurrentTime(), nm.execId, nm.flowId, false, false, nm.rate)		
		activePortal.Depart(evtMgr, nm)
		return nil
	}

	// push the message through the device and to the exgress interface
	// look up minimal delay through the device, add time for the last bit of message to clear ingress interface
	thisDev := intrfc.device
	delay := thisDev.devDelay(nm) 

	// add this message to the device's active map
	thisDev.devAddActive(&nm)
	thisDev.LogNetEvent(evtMgr.CurrentTime(), nm.execId, nm.flowId, true, false, nm.rate) 

	// advance position along route
	nm.stepIdx += 1
	nxtIntrfc := intrfcById[(*nm.route)[nm.stepIdx].srcIntrfcId]
	evtMgr.Schedule(nxtIntrfc, nm, enterEgressIntrfc, vrtime.SecondsToTime(delay))

	// event scheduler has to return _something_
	return nil
}

// passThruDelay returns the time it takes a device (switch or router)
// to perform its operation (switch or route)
func passThruDelay(dev topoDev, msg any) float64 {
	var model string

	// here hardwired, could extend to be an input argument
	var opType = "switch"

	// to look up the model we need to know whether this is a switch or router
	if dev.devType() == routerCode {
		rtrDev := dev.(*routerDev)
		model = rtrDev.routerModel
	} else {
		switchDev := dev.(*switchDev)
		model = switchDev.switchModel
	}

	if len(model) == 0 {
		model = "Default"
	}

	// if we don't have an entry for this operation, complain
	_, present := devExecTimeTbl[opType]
	if !present {
		panic(fmt.Errorf("no timing information for op type %s\n", opType))
	}

	// look up the execution time for the named operation using the name
	return devExecTimeTbl[opType][model]
}

// routeTransPerf computes the end-to-end latency for the input route,
// and the minimum bandwidth among all interfaces, devices, and networks
func routeTransitPerf(route *[]intrfcsToDev) (float64, float64) {
	// get the variables declared
	latency := float64(0.0)
	bndwdth := math.MaxFloat64 / 2.0

	// step through every intrfcsToDev entry to get the delays
	// and bandwidths it contributes
	for _, step := range *route {
		srcIntrfc := intrfcById[step.srcIntrfcId]
		dstIntrfc := intrfcById[step.dstIntrfcId]
		network := dstIntrfc.faces

		latency += srcIntrfc.state.latency
		latency += dstIntrfc.state.latency
		latency += network.netState.latency

		bndwdth = math.Min(bndwdth, srcIntrfc.availBndwdth())
		bndwdth = math.Min(bndwdth, dstIntrfc.availBndwdth())
		bndwdth = math.Min(bndwdth, network.availBndwdth())
	}

	return latency, bndwdth
}
