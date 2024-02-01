package mrnes

// sys.go has code that builds the system data structures

import (
	"fmt"
	"golang.org/x/exp/slices"
	"path"
	"sort"
	"strconv"
	"strings"
)

// declare global variables that are loaded from
// analysis of input files
var devExecTimeTbl map[string]map[string]float64

// QkNetSim is set from the command line, when selected uses 'quick' form of network simulation
var QkNetSim bool = false

// buildDevExecTimeTbl creates a map structure that stores information about
// operations on switches and routers.
//
//	The organization is
//	 map[operation type] -> map[device model] -> execution time
func buildDevExecTimeTbl(detl *DevExecList) map[string]map[string]float64 {
	det := make(map[string]map[string]float64)

	// the device timings are organized in the desc structure as a map indexed by operation type (e.g., "switch", "route")
	for opType, mapList := range detl.Times {

		// initialize the value for map[opType] if needed
		_, present := det[opType]
		if !present {
			det[opType] = make(map[string]float64)
		}

		// loop over all the records in the desc list associated with the dev op, getting and including the
		// device 'model' identifier and the execution time
		for _, devExecDesc := range mapList {
			model := devExecDesc.Model
			execTime := devExecDesc.ExecTime
			det[opType][model] = execTime
		}
	}

	// add default's for "switch" and "route"
	_, present := det["switch"]
	if !present {
		det["switch"] = make(map[string]float64)
	}
	_, present = det["switch"]["Default"]
	if !present {
		det["switch"]["Default"] = 10e-6
	}

	_, present = det["route"]
	if !present {
		det["route"] = make(map[string]float64)
	}
	_, present = det["route"]["Default"]
	if !present {
		det["route"]["Default"] = 10e-5
	}
	return det
}

// BuildExperimentNet is called from the module that creates and runs
// a simulation. Its inputs identify the names of input files, which it
// uses to assemble and initialize the model (and experiment) data structures.
func BuildExperimentNet(syn map[string]string, useYAML bool) {
	// syn is a map that binds pre-defined keys referring to input file types with file names
	// The keys are
	//	"funcExecInput"	- file describing function and device operation execution timing
	//	"topoInput"		- file describing the communication network devices and topology
	//	"expInput"		- file holding performance-oriented configuration parameters

	// call GetExperimentNetDicts to do the heavy lifting of extracting data structures
	// (typically maps) designed for serialization/deserialization,  and assign those maps to variables
	// we'll use to re-represent this information in structures optimized for run-time use
	tc, del, xd := GetExperimentNetDicts(syn)

	// panic if any one of these dictionaries could not be built
	if (tc == nil) || (del == nil) || (xd == nil) {
		panic("empty dictionary")
	}

	// populate topology data structures that enable reference to the structures just read in
	createTopoReferences(tc)

	devExecTimeTbl = buildDevExecTimeTbl(del)

	// update model experiment parameters
	setModelParameters(xd)
}

// A valueStruct type holds three different types a value might have,
// typically only one of these is used, and which one is known by context
type valueStruct struct {
	intValue    int
	floatValue  float64
	stringValue string
}

// setModelParameters takes the list of type ExpCfg and merges them
// in with a set of global variables (that may be overwritten by elements
// in this list.)
//  This set of configurations can modify values in objects whose types meet
// the paramObj interface specification.
// These are Switch, Router, Host, Network, and Interface.  For each of these
// there is a list of attributes (e.g. a Network or Interface have have either
// the 'wired' or 'wireless' attribute), including a wild card and a specific
// name for the object.   For a given object type we can order the object attributes
// in terms of the number of objects that match the attribute specification.
// For example, every object of that type matches the wild card attribute specification,
// at most one object of that type matches a name attribute specification, there
// are no more objects that match all of the attributes in a set S
// than there are that match all of the attributes in a subset of S.
// The point is, we create a list that includes defaults so that every configurable
// parameter in the model is assured of getting _some_ value, sort the list so
// that specifications that are more general appear before ones that are less general,
// and then apply the list elements one by one.  For each list element we compare
// every object of the object type specified in the list element, and for each one
// that matches we apply the given parameter value to the given parameter.

// setModelParameters takes the list of parameter configurations expressed in
// ExpCfg form, turns its elements into configuration commands that may
// initialize multiple objects, includes globally applicable assignments
// and assign these in greatest-to-least application order
func setModelParameters(expCfg *ExpCfg) {
	// this call initializes some maps used below
	GetExpParamDesc()

	// defaultParamList will hold initial ExpParameter specifications for
	// all parameter types. Some of these will be overwritten by more
	// specified assignments
	defaultParamList := make([]ExpParameter, 0)

	for _, paramObj := range ExpParamObjs {
		for _, param := range ExpParams[paramObj] {
			var vs string = ""
			switch param {
			case "switch":
				vs = "10e-6"
			case "execTime":
				vs = "100e-6"
			case "model":
				vs = "x86"
			case "media":
				vs = "wired"
			case "latency":
				vs = "10e-3"
			case "bandwidth":
				vs = "1000e6"
			case "buffer":
				vs = "100"
			case "CPU":
				vs = "x86"
			case "capacity":
				vs = "100e8"
			case "packetSize":
				vs = "1500"
			default:
				msg := fmt.Sprintf("problem %s in default default parameters\n", param)
				panic(msg)
			}

			expParam := ExpParameter{ParamObj: paramObj, Attribute: "*",
				Param: param, Value: vs}
			defaultParamList = append(defaultParamList, expParam)
		}
	}

	// defaultParamList holds the default values.
	// Now make a list from those read in from file
	expParamList := make([]ExpParameter, 0)
	expParamList = append(expParamList, expCfg.Parameters...)

	// sort the expParamList so that items with '*' as attributes have highest order,
	// and those with "name%%" have lowest.
	// Note that comparison function is embedded as an input parameter
	sort.Slice(expParamList, func(i, j int) bool {
		if expParamList[i].Attribute == "*" {
			return true
		}
		if strings.Contains(expParamList[i].Attribute, "name%%") {
			return false
		}
		return true
	})

	// concatenate defaultParamList and expParamList
	sortedParamList := append(defaultParamList, expParamList...)

	// now sort by the paramObj, to group initiations
	sort.Slice(sortedParamList,
		func(i, j int) bool { return sortedParamList[i].ParamObj < sortedParamList[j].ParamObj })

	// divide into lists for all different paramObjs
	switchList := []paramObj{}
	for _, swtch := range switchDevById {
		switchList = append(switchList, swtch)
	}

	routerList := []paramObj{}
	for _, router := range routerDevById {
		routerList = append(routerList, router)
	}

	hostList := []paramObj{}
	for _, host := range hostDevById {
		hostList = append(hostList, host)
	}

	netList := []paramObj{}
	for _, net := range networkById {
		netList = append(netList, net)
	}

	intrfcList := []paramObj{}
	for _, intrfc := range intrfcById {
		intrfcList = append(intrfcList, intrfc)
	}

	// go through the sorted list of parameter assignments, more general before more specific
	for _, param := range sortedParamList {
		// create a list that limits the objects to test to those that have required type
		var testList []paramObj
		switch param.ParamObj {
		case "Switch":
			testList = switchList
		case "Router":
			testList = routerList
		case "Host":
			testList = hostList
		case "Interface":
			testList = intrfcList
		case "Network":
			testList = netList
		}

		// for every object in the constrained list test whether the attributes match.
		// Observe that
		//	 - * denotes a wild card
		//   - a set of attributes all of which need to be matched by the object
		//     is expressed as a comma-separated list
		//   - If a name "Fred" is given as an attribute, what is specified is "name%%Fred"
		for _, testObj := range testList {
			// separate out the items in a comma-separated list
			attrbs := strings.Split(param.Attribute, ",")

			var matched bool = true
			var vs valueStruct

			// every attribute must match.  Variable 'matched' is initialized to say that they do
			for _, attrb := range attrbs {
				// the parameter value might be a string, or float, or bool.
				// stringToValue figures it out and returns value assignment in vs
				vs = stringToValueStruct(param.Value)

				// wild card means set.  Should be the case that if '*' is present
				// there is nothing else, but '*' overrides all
				if strings.Contains(attrb, "*") {
					matched = true

					break
				}

				// recognize name attribute specification and strip it off
				if strings.Contains(attrb, "name%%") {
					pieces := strings.Split(attrb, "name%%")

					// paramObj interface lets us get the name of objects from different types
					if pieces[1] == testObj.paramObjName() {
						testObj.setParam(param.Param, vs)

						continue
					} else {
						matched = false

						break
					}
				}
				// if any of the attributes don't match we don't match
				if !testObj.matchParam(attrb) {
					matched = false

					continue
				}
			}

			// this object passed the match test so apply the parameter value
			if matched {
				testObj.setParam(param.Param, vs)
			}
		}
	}
}

// stringToValueStruct takes a string (used in the run-time configuration phase)
// and determines whether it is an integer, floating point, or a string
func stringToValueStruct(v string) valueStruct {
	vs := valueStruct{intValue: 0, floatValue: 0.0, stringValue: ""}

	// try conversion to int
	ivalue, ierr := strconv.Atoi(v)
	if ierr == nil {
		vs.intValue = ivalue
		vs.floatValue = float64(ivalue)
		return vs
	}

	// failing that, try conversion to float
	fvalue, ferr := strconv.ParseFloat(v, 64)
	if ferr == nil {
		vs.floatValue = fvalue
		return vs
	}

	// left with it being a string
	vs.stringValue = v
	return vs
}

// global variables for finding things given an id, or a name
var paramObjById map[int]paramObj
var paramObjByName map[string]paramObj

var routerDevById map[int]*routerDev
var routerDevByName map[string]*routerDev

var hostDevById map[int]*hostDev
var hostDevByName map[string]*hostDev

var switchDevById map[int]*switchDev
var switchDevByName map[string]*switchDev

var networkById map[int]*networkStruct
var networkByName map[string]*networkStruct

var brdcstDmnById map[int]*brdcstDmnStruct
var brdcstDmnByName map[string]*brdcstDmnStruct

var intrfcById map[int]*intrfcStruct
var intrfcByName map[string]*intrfcStruct

var topoDevById map[int]topoDev
var topoDevByName map[string]topoDev

var topoGraph map[int][]int

// utility function for generating unique integer ids on demand
var numIds int = 0

// nxtId creates an id for objects created within mrnes module that are unique among those objects
func nxtId() int {
	numIds += 1
	return numIds
}

// GetExperimentNetDicts accepts a map that holds the names of the input files used for the network part of an experiment
// creates internal representations of the information they hold, and returns those structs.
func GetExperimentNetDicts(syn map[string]string) (*TopoCfg, *DevExecList, *ExpCfg) {
	var tc *TopoCfg
	var del *DevExecList
	var xd *ExpCfg

	var empty []byte = make([]byte, 0)

	var errs []error
	var err error

	var useYAML bool
	ext := path.Ext(syn["topoInput"])
	useYAML = (ext == ".yaml") || (ext == ".yml")

	tc, err = ReadTopoCfg(syn["topoInput"], useYAML, empty)
	errs = append(errs, err)

	ext = path.Ext(syn["devExecInput"])
	useYAML = (ext == ".yaml") || (ext == ".yml")

	del, err = ReadDevExecList(syn["devExecInput"], useYAML, empty)
	errs = append(errs, err)

	ext = path.Ext(syn["expInput"])
	useYAML = (ext == ".yaml") || (ext == ".yml")

	xd, err = ReadExpCfg(syn["expInput"], useYAML, empty)
	errs = append(errs, err)

	err = ReportErrs(errs)
	if err != nil {
		panic(err)
	}
	// ensure that the configuration parameters lists are built
	GetExpParamDesc()

	return tc, del, xd
}

// createTopoReferences reads from the input TopoCfg file to create references
func createTopoReferences(topoCfg *TopoCfg) {
	// initialize the maps and slices used for object lookup
	topoDevById = make(map[int]topoDev)
	topoDevByName = make(map[string]topoDev)

	paramObjById = make(map[int]paramObj)
	paramObjByName = make(map[string]paramObj)

	hostDevById = make(map[int]*hostDev)
	hostDevByName = make(map[string]*hostDev)

	switchDevById = make(map[int]*switchDev)
	switchDevByName = make(map[string]*switchDev)

	routerDevById = make(map[int]*routerDev)
	routerDevByName = make(map[string]*routerDev)

	networkById = make(map[int]*networkStruct)
	networkByName = make(map[string]*networkStruct)

	brdcstDmnById = make(map[int]*brdcstDmnStruct)
	brdcstDmnByName = make(map[string]*brdcstDmnStruct)

	intrfcById = make(map[int]*intrfcStruct)
	intrfcByName = make(map[string]*intrfcStruct)

	topoGraph = make(map[int][]int)

	// fetch the router	descriptions
	for _, rtr := range topoCfg.Routers {
		// create a runtime representation from its desc representation
		rtrDev := createRouterDev(&rtr)

		// get name and id
		rtrName := rtrDev.routerName
		rtrId := rtrDev.routerId

		// add rtrDev to topoDev map

		// save rtrDev for lookup by Id and Name

		// for topoDev interface
		addTopoDevLookup(rtrId, rtrName, rtrDev)
		routerDevById[rtrId] = rtrDev
		routerDevByName[rtrName] = rtrDev

		// for paramObj interface
		paramObjById[rtrId] = rtrDev
		paramObjByName[rtrName] = rtrDev
	}

	// fetch the switch descriptions
	for _, swtch := range topoCfg.Switches {
		// create a runtime representation from its desc representation
		switchDev := createSwitchDev(&swtch)

		// get name and id
		switchName := switchDev.switchName
		switchId := switchDev.switchId

		// save switchDev for lookup by Id and Name

		// for topoDev interface
		addTopoDevLookup(switchId, switchName, switchDev)
		switchDevById[switchId] = switchDev
		switchDevByName[switchName] = switchDev

		// for paramObj interface
		paramObjById[switchId] = switchDev
		paramObjByName[switchName] = switchDev
	}

	// fetch the host descriptions
	for _, host := range topoCfg.Hosts {
		// create a runtime representation from its desc representation
		hostDev := createHostDev(&host)

		// get name and id
		hostName := hostDev.hostName
		hostId := hostDev.hostId

		// save hostDev for lookup by Id and Name

		// for topoDev interface
		addTopoDevLookup(hostId, hostName, hostDev)
		hostDevById[hostId] = hostDev
		hostDevByName[hostName] = hostDev

		// for paramObj interface
		paramObjById[hostId] = hostDev
		paramObjByName[hostName] = hostDev
	}

	// fetch the network descriptions
	for _, netDesc := range topoCfg.Networks {
		// create a runtime representation from its desc representation
		net := createNetworkStruct(&netDesc)

		// save pointer to net accessible by id or name
		networkById[net.number] = net
		networkByName[net.name] = net

		// for paramObj interface
		paramObjById[net.number] = net
		paramObjByName[net.name] = net
	}

	// fetch the broadcast domain descriptions
	for _, bd := range topoCfg.BroadcastDomains {
		// create a runtime representation from its desc representation
		bcd := createBrdcstDmnStruct(&bd)
		brdcstDmnById[bcd.number] = bcd
		brdcstDmnByName[bd.Name] = bcd
	}

	// include lists of interfaces for each device
	for _, rtrDesc := range topoCfg.Routers {
		for _, intrfc := range rtrDesc.Interfaces {

			// create a runtime representation from its desc representation
			is := createIntrfcStruct(&intrfc)

			// save is for reference by id or name
			intrfcById[is.number] = is
			intrfcByName[intrfc.Name] = is

			// for paramObj interface
			paramObjById[is.number] = is
			paramObjByName[intrfc.Name] = is

			// look up the hosting router, name froun in desc description
			rtr := routerDevByName[rtrDesc.Name]
			rtr.addIntrfc(is)
		}
	}

	for _, hostDesc := range topoCfg.Hosts {
		for _, intrfc := range hostDesc.Interfaces {
			// create a runtime representation from its desc representation
			is := createIntrfcStruct(&intrfc)

			// save is for reference by id or name
			intrfcById[is.number] = is
			intrfcByName[intrfc.Name] = is

			// for paramObj interface
			paramObjById[is.number] = is
			paramObjByName[intrfc.Name] = is

			// look up hosting host, use not from host's desc representation
			host := hostDevByName[hostDesc.Name]
			host.addIntrfc(is)
		}
	}

	for _, switchDesc := range topoCfg.Switches {
		for _, intrfc := range switchDesc.Interfaces {
			// create a runtime representation from its desc representation
			is := createIntrfcStruct(&intrfc)

			// save is for reference by id or name
			intrfcById[is.number] = is
			intrfcByName[intrfc.Name] = is

			// for paramObj interface
			paramObjById[is.number] = is
			paramObjByName[intrfc.Name] = is

			// look up hosting switch, using switch name from desc
			// representation
			swtch := switchDevByName[switchDesc.Name]
			swtch.addIntrfc(is)
		}
	}

	// link the connect fields, now that all interfaces are known

	// loop over routers
	for _, rtrDesc := range topoCfg.Routers {
		// loop over interfaces the router hosts
		for _, intrfc := range rtrDesc.Interfaces {
			// link the run-time representation of this interface to the
			// run-time representation of the interface it connects, if any
			// set the run-time pointer to the network faced by the interface
			linkIntrfcStruct(&intrfc)
		}
	}

	// loop over hosts
	for _, hostDesc := range topoCfg.Hosts {
		// loop over interfaces the host hosts
		for _, intrfc := range hostDesc.Interfaces {
			// link the run-time representation of this interface to the
			// run-time representation of the interface it connects, if any
			// set the run-time pointer to the network faced by the interface
			linkIntrfcStruct(&intrfc)
		}
	}

	// loop over switches
	for _, switchDesc := range topoCfg.Switches {
		// loop over interfaces the switch hosts
		for _, intrfc := range switchDesc.Interfaces {
			// link the run-time representation of this interface to the
			// run-time representation of the interface it connects, if any
			// set the run-time pointer to the network faced by the interface
			linkIntrfcStruct(&intrfc)
		}
	}

	// networks and broadcast domains have slices with pointers with things that
	// we know now are initialized, so can finish the initialization

	// loop over networks
	for _, netd := range topoCfg.Networks {
		// find the run-time representation of the network
		net := networkByName[netd.Name]

		// initialize it from the desc description of the network
		net.initNetworkStruct(&netd)
	}

	// loop over BCDs
	for _, bd := range topoCfg.BroadcastDomains {
		// look up run-time representation of BCD
		bcd := brdcstDmnByName[bd.Name]

		// initialize it from the desc description of the BCD
		bcd.initBrdcstDmnStruct(&bd)
	}

	// build a graph whose leaf nodes are hosts, interior nodes are switches and routers.
	// edges are defined by network connectivity.  Graph is used to discover
	// routes.  Format of map node is map[int][]int, where the integer index is the id of
	// the devices, and []int is a list of ids of devices to which it can directly communicate
	// through a common network

	// Create nodes representing hosts
	// Loop over hosts
	for hostId, host := range hostDevById {
		// initialize the node's list of peers
		topoGraph[hostId] = []int{}

		// hosts connect only to BCD hubs. Note a host may be part of multiple BCDs
		// (through different interfaces)
		for _, bcdName := range host.hostBrdcstDmn {
			// look up the run-time BCD
			bcd := brdcstDmnByName[bcdName]

			// fetch its id
			hubId := bcd.bcdHub.devId()

			// add the edge from host to hub.   We'll catch the reverse when we focus on the hub device
			topoGraph[hostId] = append(topoGraph[hostId], hubId)
		}
	}

	// create nodes representing switches
	// loop over switches
	for switchId, swtch := range switchDevById {
		// initialize the node's list of peers
		topoGraph[switchId] = []int{}

		// touch every interface in the switch and link to devices reached through it
		for _, intrfc := range swtch.switchIntrfcs {
			// connection exist?
			if intrfc.connects != nil {
				// yes, get the id of the device hosting the interface connected to
				peerId := intrfc.connects.device.devId()

				// append that id to the switch nodes list of peers
				topoGraph[switchId] = append(topoGraph[switchId], peerId)
			}
			// switches only have wired interfaces so there is no need to consider alternatives
		}
	}

	// create nodes representing routers
	// loop over routers
	for rtrId, rtr := range routerDevById {
		// initialize the node's list of peers
		topoGraph[rtrId] = []int{}

		// a router that is a wireless hub connects to all the hosts in its broadcast domain.
		// a router that is a wireless hub holds the name of the BCD it hubs in its routerBrdcstDmn string
		if len(rtr.routerBrdcstDmn) > 0 {
			// it's a hub.  Look up the run-time representation of the BCD
			brdcstDmn := brdcstDmnByName[rtr.routerBrdcstDmn]

			// the (wireless) hub router connects to every host in the BCD
			for _, host := range brdcstDmn.bcdHosts {
				topoGraph[rtrId] = append(topoGraph[rtrId], host.hostId)
			}

			// a switch may have wired connections to routers, consider those
			// look at the connect field of every interface.  Non-nil means there's
			// a wire to another device.
			for _, intrfc := range rtr.routerIntrfcs {
				if intrfc.connects != nil {

					// to get the other device Id follow the 'connects' connection to the
					// connected interface and then that interface's 'device' pointer to the
					// device, whose id we can get through topoDev interface function devId()
					connDevId := intrfc.connects.device.devId()
					topoGraph[rtrId] = append(topoGraph[rtrId], connDevId)
				}
			}
		} else {
			// the router is not a wireless hub, so it faces one or more networks.
			// make a link to each non-hub router on each of these networks

			// look at every interface the router hosts
			for _, intrfc := range rtr.routerIntrfcs {
				// is the interface wired?
				if intrfc.connects != nil {
					// yes, so get the id of the device at the other end of the connection
					connDevId := intrfc.connects.device.devId()
					topoGraph[rtrId] = append(topoGraph[rtrId], connDevId)
				}

				// an interface always points to a nework, so we can loop
				// over the routers that network has and connect to them
				// (except self, of course)

				// touch every router on the network the interface faces
				for _, xrtr := range intrfc.faces.netRouters {
					// skip if that router is the one hosting the interface
					if rtrId == xrtr.routerId {
						continue
					}

					// skip if the router is a wireless hub
					if len(xrtr.routerBrdcstDmn) > 0 {
						continue
					}

					// add to the list if it is not already present there
					peerId := xrtr.routerId
					if !slices.Contains(topoGraph[rtrId], peerId) {
						topoGraph[rtrId] = append(topoGraph[rtrId], peerId)
					}
				}
			}
		}
	}
}

// addTopoDevLookup puts a new entry in the topoDevById and topoDevByName
// maps if that entry does not already exist
func addTopoDevLookup(tdId int, tdName string, td topoDev) {
	_, present1 := topoDevById[tdId]
	if present1 {
		msg := fmt.Sprintf("index %d over-used in topoDevById\n", tdId)
		panic(msg)
	}
	_, present2 := topoDevByName[tdName]
	if present2 {
		msg := fmt.Sprintf("name %s over-used in topoDevByName\n", tdName)
		panic(msg)
	}

	topoDevById[tdId] = td
	topoDevByName[tdName] = td
}
