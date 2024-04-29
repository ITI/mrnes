package mrnes

// sys.go has code that builds the system data structures

import (
	"fmt"
	"golang.org/x/exp/slices"
	"github.com/iti/evt/evtm"
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

var devTraceMgr *TraceManager

// intfrastructure for inter-func addressing (including x-compPattern addressing)
type MrnesApp interface {

	// a globally unique name for the application
	GlobalName() string

	// an event handler to call to present a message to an app
	ArrivalFunc() evtm.EventHandlerFunction
}

// NullHandler exists to provide as a link for data fields that call for
// an event handler, but no event handler is actually needed
func NullHandler(evtMgr *evtm.EventManager, context any, msg any) any {
	return nil
}	

// BuildExperimentNet is called from the module that creates and runs
// a simulation. Its inputs identify the names of input files, which it
// uses to assemble and initialize the model (and experiment) data structures.
func BuildExperimentNet(syn map[string]string, useYAML bool, idCounter int, traceMgr *TraceManager) {
	// syn is a map that binds pre-defined keys referring to input file types with file names
	// call GetExperimentNetDicts to do the heavy lifting of extracting data structures
	// (typically maps) designed for serialization/deserialization,  and assign those maps to variables
	// we'll use to re-represent this information in structures optimized for run-time use
	tc, del, xd, xdx := GetExperimentNetDicts(syn)

	// panic if any one of these dictionaries could not be built
	if (tc == nil) || (del == nil) || (xd == nil) {
		panic("empty dictionary")
	}

	NumIds = idCounter

	// populate topology data structures that enable reference to the structures just read in
	createTopoReferences(tc, traceMgr)

	// save as global to mrnes for tracing network execution
	devTraceMgr = traceMgr

	devExecTimeTbl = buildDevExecTimeTbl(del)

	// update model experiment parameters
	setModelParameters(xd, xdx)

	// see whether connections give fully connected graph or not
	checkConnections(topoGraph) 
}

// A valueStruct type holds three different types a value might have,
// typically only one of these is used, and which one is known by context
type valueStruct struct {
	intValue    int
	floatValue  float64
	stringValue string
	boolValue   bool
}

// reorderExpParams is used to put the ExpParameter parameters in 
// an order such that the earlier elements in the order have broader
// range of attributes than later ones that apply to the same configuration element.
// This is entirely the same idea as is the approach of choosing a routing rule that has the
// smallest subnet range, when multiple rules apply to the same IP address
func reorderExpParams(pL []ExpParameter) []ExpParameter {
	// partition the list into three sublists: wildcard (wc), single (sg), and named (nm).
	// The wildcard elements always appear before any others, and the named elements always
	// appear after all the others.
	wc := []ExpParameter{}
	nm := []ExpParameter{}
	sg := []ExpParameter{}

	// assign wc, sg, or nm based on attribute
	for _, param := range pL {
		assigned := false
	
		// each parameter assigned to one of three lists
		for _, attrb := range param.Attributes {
			// wildcard list?
			if attrb.AttrbName == "*" {
				wc = append(wc, param)
				assigned = true
				break
			// name list?
			} else if attrb.AttrbName == "name" {
				nm = append(nm, param)
				assigned = true
				break
			}
		}
		// all attributes checked and none whose names are "*" or "name"
		if !assigned {
			sg = append(sg, param)
		}	
	}
		
	// we do further rearrangement to bring identical elements together for detection and cleanup.
	// The wild card entries are identical in the ParamObj and Attribute fields, so order them based on the parameter.
	sort.Slice(wc, func(i,j int) bool { return wc[i].Param < wc[j].Param } )

	// sort the sg elements by (Attribute, Param) key
	sort.Slice(sg, func(i,j int) bool { 
		compared := CompareAttrbs(sg[i].Attributes, sg[j].Attributes)
		if compared == -1 {
			return true
		} else if compared == 1 {
			return false
		}
		/*
		if sg[i].Attribute < sg[j].Attribute {
			return true
		}
		if sg[i].Attribute > sg[j].Attribute {
			return false
		}
		*/
		if sg[i].Param < sg[j].Param  {
			return true
		}
		if sg[i].Param > sg[j].Param {
			return false
		}
		return sg[i].Value < sg[j].Value })

	// sort the named elements by the (Attribute, Param) key
	sort.Slice(nm, func(i,j int) bool { 
		compared := CompareAttrbs(nm[i].Attributes, nm[j].Attributes)
		if compared == -1 {
			return true
		} else if compared == 1 {
			return false
		}

		/*
		if nm[i].Attribute < nm[j].Attribute {
			return true
		}
		if nm[i].Attribute > nm[j].Attribute {
			return false
		}
		*/
		if nm[i].Param < nm[j].Param {
			return true
		}
		if nm[i].Param > nm[j].Param {
			return false
		}
		return nm[i].Value < nm[j].Value} )
 

	// pull them together with wc first, followed by sg, and finally nm
	wc = append(wc, sg...)
	wc = append(wc, nm...)

	// get rid of duplicates
	for idx:= len(wc)-1; idx > 0; idx = idx-1 {
		if wc[idx].Eq(&wc[idx-1]) {
			wc = append(wc[:idx],wc[(idx+1):]...)
		}
	}

	return wc
}

// setModelParameters takes the list of parameter configurations expressed in
// ExpCfg form, turns its elements into configuration commands that may
// initialize multiple objects, includes globally applicable assignments
// and assign these in greatest-to-least application order
func setModelParameters(expCfg, expxCfg *ExpCfg) {
	// this call initializes some maps used below
	GetExpParamDesc()

	// defaultParamList will hold initial ExpParameter specifications for
	// all parameter types. Some of these will be overwritten by more
	// specified assignments
	defaultParamList := make([]ExpParameter, 0)

	// set defaults to ensure that every parameter that has to have a value does
	for _, paramObj := range ExpParamObjs {
		for _, param := range ExpParams[paramObj] {
			var vs string = ""
			switch param {
			case "switch":
				vs = "10e-6"
			case "model":
				vs = "cisco"
			case "latency":
				vs = "10e-3"
			case "delay":
				vs = "10e-6"
			case "bandwidth":
				vs = "10"
			case "buffer":
				vs = "100"
			case "CPU":
				vs = "i7"
			case "capacity":
				vs = "10"
			case "MTU":
				vs = "1500"
			case "trace":
				vs = "false"
			default:
				msg := fmt.Sprintf("problem %s in default default parameters\n", param)
				panic(msg)
			}

			wcAttrb := []AttrbStruct{AttrbStruct{AttrbName:"*", AttrbValue: ""}}
			expParam := ExpParameter{ParamObj: paramObj, Attributes: wcAttrb, Param: param, Value: vs}
			defaultParamList = append(defaultParamList, expParam)
		}
	}
	
	// separate the parameters into the ParamObj groups they apply to
	endptParams := []ExpParameter{}
	filterParams := []ExpParameter{}
	netParams := []ExpParameter{}
	rtrParams := []ExpParameter{}
	swtchParams := []ExpParameter{}
	intrfcParams := []ExpParameter{}

	endptParamsMod := []ExpParameter{}
	filterParamsMod := []ExpParameter{}
	netParamsMod := []ExpParameter{}
	rtrParamsMod := []ExpParameter{}
	swtchParamsMod := []ExpParameter{}
	intrfcParamsMod := []ExpParameter{}

	for _, param := range expCfg.Parameters {
		switch param.ParamObj {
			case "Endpt":
				endptParams = append(endptParams, param)
			case "Router":
				rtrParams = append(rtrParams, param)
			case "Switch":
				swtchParams = append(swtchParams, param)
			case "Interface":
				intrfcParams = append(intrfcParams, param)
			case "Network":
				netParams = append(netParams, param)
			case "Filter":
				filterParams = append(filterParams, param)
			default :
				panic("surprise ParamObj")
		}
	}

	modExpPresent := (expxCfg != nil)

	if modExpPresent {
		for _, param := range expxCfg.Parameters {
			switch param.ParamObj {
				case "Endpt":
					endptParamsMod = append(endptParamsMod, param)
				case "Router":
					rtrParamsMod = append(rtrParamsMod, param)
				case "Switch":
					swtchParamsMod = append(swtchParamsMod, param)
				case "Interface":
					intrfcParamsMod = append(intrfcParamsMod, param)
				case "Network":
					netParamsMod = append(netParamsMod, param)
				case "Filter":
					filterParamsMod = append(filterParamsMod, param)
				default :
					panic("surprise ParamObj")
			}
		}
	}

	// reorder each list to assure the application order of most-general-first, and remove duplicates
	endptParams = reorderExpParams(endptParams)
	filterParams = reorderExpParams(filterParams)
	rtrParams = reorderExpParams(rtrParams)
	swtchParams = reorderExpParams(swtchParams)
	intrfcParams = reorderExpParams(intrfcParams)
	netParams = reorderExpParams(netParams)
	
	endptParamsMod = reorderExpParams(endptParamsMod)
	filterParamsMod = reorderExpParams(filterParamsMod)
	rtrParamsMod = reorderExpParams(rtrParamsMod)
	swtchParamsMod = reorderExpParams(swtchParamsMod)
	intrfcParamsMod = reorderExpParams(intrfcParamsMod)
	netParamsMod = reorderExpParams(netParamsMod)
	
	// concatenate defaultParamList and these lists.  Note that this places the defaults
	// we created above before any defaults read in from file, so that if there are conflicting
	// default assignments the one the user put in the startup file will be applied after the
	// default default we create in this program
	orderedParamList := append(defaultParamList, endptParams...)
	orderedParamList = append(orderedParamList, endptParamsMod...)

	orderedParamList = append(orderedParamList, filterParams...)
	orderedParamList = append(orderedParamList, filterParamsMod...)

	orderedParamList = append(orderedParamList, rtrParams...)
	orderedParamList = append(orderedParamList, rtrParamsMod...)

	orderedParamList = append(orderedParamList, swtchParams...)
	orderedParamList = append(orderedParamList, swtchParamsMod...)

	orderedParamList = append(orderedParamList, intrfcParams...)
	orderedParamList = append(orderedParamList, intrfcParamsMod...)

	orderedParamList = append(orderedParamList, netParams...)
	orderedParamList = append(orderedParamList, netParamsMod...)

	// get the names of all network objects, separated by their network object type
	switchList := []paramObj{}
	for _, swtch := range switchDevById {
		switchList = append(switchList, swtch)
	}

	routerList := []paramObj{}
	for _, router := range routerDevById {
		routerList = append(routerList, router)
	}

	endptList := []paramObj{}
	for _, endpt := range endptDevById {
		endptList = append(endptList, endpt)
	}

	filterList := []paramObj{}
	for _, filter := range filterDevById {
		filterList = append(filterList, filter)
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
	for _, param := range orderedParamList {
		// create a list that limits the objects to test to those that have required type
		var testList []paramObj

		switch param.ParamObj {
			case "Switch":
				testList = switchList
			case "Router":
				testList = routerList
			case "Endpt":
				testList = endptList
			case "Interface":
				testList = intrfcList
			case "Network":
				testList = netList
			case "Filter":
				testList = filterList
		}

		// for every object in the constrained list test whether the attributes match.
		// Observe that
		//	 - * denotes a wild card
		//   - a set of attributes all of which need to be matched by the object
		//     is expressed as a comma-separated list
		//   - If a name "Fred" is given as an attribute, what is specified is "name%%Fred"
		for _, testObj := range testList {
			// separate out the items in a comma-separated list

			var matched bool = true
			for _, attrb := range param.Attributes {
				attrbName := attrb.AttrbName
				attrbValue := attrb.AttrbValue

				// wild card means set.  Should be the case that if '*' is present
				// there is nothing else, but '*' overrides all
				if attrbName == "*" {
					matched = true

					break
				}


				// if any of the attributes don't match we don't match
				if !testObj.matchParam(attrbName, attrbValue) {
					matched = false
					break
				}
			}

			// this object passed the match test so apply the parameter value
			if matched {
				// the parameter value might be a string, or float, or bool.
				// stringToValue figures it out and returns value assignment in vs
				vs := stringToValueStruct(param.Value)
				testObj.setParam(param.Param, vs)
			}
		}
	}
}

// stringToValueStruct takes a string (used in the run-time configuration phase)
// and determines whether it is an integer, floating point, or a string
func stringToValueStruct(v string) valueStruct {
	vs := valueStruct{intValue: 0, floatValue: 0.0, stringValue: "", boolValue: false}

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

	// left with it being a string.  See if true, True
	if v == "true" || v == "True" {
		vs.boolValue = true
		return vs
	}

	vs.stringValue = v
	return vs
}

// global variables for finding things given an id, or a name
var paramObjById map[int]paramObj
var paramObjByName map[string]paramObj

var routerDevById map[int]*routerDev
var routerDevByName map[string]*routerDev

var endptDevById map[int]*endptDev
var endptDevByName map[string]*endptDev

var filterDevById map[int]*filterDev
var filterDevByName map[string]*filterDev

var switchDevById map[int]*switchDev
var switchDevByName map[string]*switchDev

var networkById map[int]*networkStruct
var networkByName map[string]*networkStruct

var intrfcById map[int]*intrfcStruct
var intrfcByName map[string]*intrfcStruct

var topoDevById map[int]topoDev
var topoDevByName map[string]topoDev

var topoGraph map[int][]int

// utility function for generating unique integer ids on demand
var NumIds int = 0

// nxtId creates an id for objects created within mrnes module that are unique among those objects
func nxtId() int {
	NumIds += 1
	return NumIds
}

// GetExperimentNetDicts accepts a map that holds the names of the input files used for the network part of an experiment
// creates internal representations of the information they hold, and returns those structs.
func GetExperimentNetDicts(syn map[string]string) (*TopoCfg, *DevExecList, *ExpCfg, *ExpCfg) {
	var tc *TopoCfg
	var del *DevExecList
	var xd, xdx *ExpCfg

	var empty []byte = make([]byte, 0)

	var errs []error
	var err error

	var useYAML bool

	ext := path.Ext(syn["topo"])
	useYAML = (ext == ".yaml") || (ext == ".yml")

	tc, err = ReadTopoCfg(syn["topo"], useYAML, empty)
	errs = append(errs, err)

	ext = path.Ext(syn["devExec"])
	useYAML = (ext == ".yaml") || (ext == ".yml")

	del, err = ReadDevExecList(syn["devExec"], useYAML, empty)
	errs = append(errs, err)

	ext = path.Ext(syn["exp"])
	useYAML = (ext == ".yaml") || (ext == ".yml")

	xd, err = ReadExpCfg(syn["exp"], useYAML, empty)
	errs = append(errs, err)

	if len(syn["mdfy"]) > 0 {
		ext = path.Ext(syn["mdfy"])
		useYAML = (ext == ".yaml") || (ext == ".yml")
		xdx, err = ReadExpCfg(syn["mdfy"], useYAML, empty)
		errs = append(errs, err)
	}

	err = ReportErrs(errs)
	if err != nil {
		panic(err)
	}
	// ensure that the configuration parameters lists are built
	GetExpParamDesc()

	return tc, del, xd, xdx
}

// connectIds remembers the asserted communication linkage between
// devices with given id numbers through modification of the input map tg
func connectIds(tg map[int][]int, id1, id2, intrfc1, intrfc2 int) {
	/*
	if (intrfc1 == 14 && intrfc2 == 20) || (intrfc1 == 20 && intrfc2 == 14) ||
		(intrfc1 == 14 && intrfc2 == 21) || (intrfc1 == 21 && intrfc2 == 14) {
			fmt.Println("Trapped")
		}
		*/
	if routeStepIntrfcs == nil {
		routeStepIntrfcs = make(map[intPair]intPair)
	}
	// don't save connections to self if offered
	if id1==id2 {
		return
	}

	// add id2 to id1's list of peers, if not already present
	if !slices.Contains(tg[id1], id2) {
		tg[id1] = append(tg[id1], id2)
	}

	// add id1 to id2's list of peers, if not already present
	if !slices.Contains(tg[id2], id1) {
		tg[id2] = append(tg[id2], id1)
	}
	routeStepIntrfcs[intPair{i:id1,j:id2}] = intPair{i:intrfc1, j:intrfc2}
}

// createTopoReferences reads from the input TopoCfg file to create references
func createTopoReferences(topoCfg *TopoCfg, tm *TraceManager) {
	// initialize the maps and slices used for object lookup
	topoDevById = make(map[int]topoDev)
	topoDevByName = make(map[string]topoDev)

	paramObjById = make(map[int]paramObj)
	paramObjByName = make(map[string]paramObj)

	endptDevById = make(map[int]*endptDev)
	endptDevByName = make(map[string]*endptDev)

	filterDevById = make(map[int]*filterDev)
	filterDevByName = make(map[string]*filterDev)

	switchDevById = make(map[int]*switchDev)
	switchDevByName = make(map[string]*switchDev)

	routerDevById = make(map[int]*routerDev)
	routerDevByName = make(map[string]*routerDev)

	networkById = make(map[int]*networkStruct)
	networkByName = make(map[string]*networkStruct)

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

		// store id -> name for trace
		tm.AddName(rtrId, rtrName, "router")
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

		// store id -> name for trace
		tm.AddName(switchId, switchName, "switch")
	}

	// fetch the endpt descriptions
	for _, endpt := range topoCfg.Endpts {
		// create a runtime representation from its desc representation
		endptDev := createEndptDev(&endpt)

		// get name and id
		endptName := endptDev.endptName
		endptId := endptDev.endptId

		// save endptDev for lookup by Id and Name

		// for topoDev interface
		addTopoDevLookup(endptId, endptName, endptDev)
		endptDevById[endptId] = endptDev
		endptDevByName[endptName] = endptDev

		// for paramObj interface
		paramObjById[endptId] = endptDev
		paramObjByName[endptName] = endptDev

		// store id -> name for trace
		tm.AddName(endptId, endptName, "endpt")
	}

	// fetch the filter descriptions
	for _, filter := range topoCfg.Filters {
		// create a runtime representation from its desc representation
		filterDev := createFilterDev(&filter)

		// get name and id
		filterName := filterDev.filterName
		filterId := filterDev.filterId

		// save filterDev for lookup by Id and Name
		addTopoDevLookup(filterId, filterName, filterDev)
		filterDevById[filterId] = filterDev
		filterDevByName[filterName] = filterDev

		// for paramObj interface
		paramObjById[filterId] = filterDev
		paramObjByName[filterName] = filterDev

		// store id -> name for trace
		tm.AddName(filterId, filterName, "filter")
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

		// store id -> name for trace
		tm.AddName(net.number, net.name, "network")
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

			// store id -> name for trace
			tm.AddName(is.number, intrfc.Name, "interface")

			rtr := routerDevByName[rtrDesc.Name]
			rtr.addIntrfc(is)
		}
	}
	
	for _, filterDesc := range topoCfg.Filters {
		for _, intrfc := range filterDesc.Interfaces {

			// create a runtime representation from its desc representation
			is := createIntrfcStruct(&intrfc)

			// save is for reference by id or name
			intrfcById[is.number] = is
			intrfcByName[intrfc.Name] = is

			// for paramObj interface
			paramObjById[is.number] = is
			paramObjByName[intrfc.Name] = is

			// store id -> name for trace
			tm.AddName(is.number, intrfc.Name, "interface")

			filter := filterDevByName[filterDesc.Name]
			filter.addIntrfc(is)
		}
	}

	for _, endptDesc := range topoCfg.Endpts {
		for _, intrfc := range endptDesc.Interfaces {
			// create a runtime representation from its desc representation
			is := createIntrfcStruct(&intrfc)

			// save is for reference by id or name
			intrfcById[is.number] = is
			intrfcByName[intrfc.Name] = is

			// store id -> name for trace
			tm.AddName(is.number, intrfc.Name, "interface")

			// for paramObj interface
			paramObjById[is.number] = is
			paramObjByName[intrfc.Name] = is

			// look up endpting endpt, use not from endpt's desc representation
			endpt := endptDevByName[endptDesc.Name]
			endpt.addIntrfc(is)
		}
	}

	for _, switchDesc := range topoCfg.Switches {
		for _, intrfc := range switchDesc.Interfaces {
			// create a runtime representation from its desc representation
			is := createIntrfcStruct(&intrfc)

			// save is for reference by id or name
			intrfcById[is.number] = is
			intrfcByName[intrfc.Name] = is

			// store id -> name for trace
			tm.AddName(is.number, intrfc.Name, "interface")

			// for paramObj interface
			paramObjById[is.number] = is
			paramObjByName[intrfc.Name] = is

			// look up endpting switch, using switch name from desc
			// representation
			swtch := switchDevByName[switchDesc.Name]
			swtch.addIntrfc(is)
		}
	}

	// link the connect fields, now that all interfaces are known
	// loop over routers
	for _, rtrDesc := range topoCfg.Routers {
		// loop over interfaces the router endpts
		for _, intrfc := range rtrDesc.Interfaces {
			// link the run-time representation of this interface to the
			// run-time representation of the interface it connects, if any
			// set the run-time pointer to the network faced by the interface
			linkIntrfcStruct(&intrfc)
		}
	}

	// link the connect fields, now that all interfaces are known
	// loop over filters 
	for _, filterDesc := range topoCfg.Filters {
		// loop over interfaces the filter endpts
		for _, intrfc := range filterDesc.Interfaces {
			// link the run-time representation of this interface to the
			// run-time representation of the interface it connects, if any
			// set the run-time pointer to the network faced by the interface
			linkIntrfcStruct(&intrfc)
		}
	}

	// loop over endpts
	for _, endptDesc := range topoCfg.Endpts {
		// loop over interfaces the endpt endpts
		for _, intrfc := range endptDesc.Interfaces {
			// link the run-time representation of this interface to the
			// run-time representation of the interface it connects, if any
			// set the run-time pointer to the network faced by the interface
			linkIntrfcStruct(&intrfc)
		}
	}

	// loop over switches
	for _, switchDesc := range topoCfg.Switches {
		// loop over interfaces the switch endpts
		for _, intrfc := range switchDesc.Interfaces {
			// link the run-time representation of this interface to the
			// run-time representation of the interface it connects, if any
			// set the run-time pointer to the network faced by the interface
			linkIntrfcStruct(&intrfc)
		}
	}

	// networks have slices with pointers with things that
	// we know now are initialized, so can finish the initialization

	// loop over networks
	for _, netd := range topoCfg.Networks {
		// find the run-time representation of the network
		net := networkByName[netd.Name]

		// initialize it from the desc description of the network
		net.initNetworkStruct(&netd)
	}

	// put all the connections recorded in the Cabled and Wireless fields into the topoGraph
	for _, dev := range topoDevById {
		devId := dev.devId()
		for _, intrfc := range dev.devIntrfcs() {
			connected := false
			if intrfc.cable != nil && compatibleIntrfcs(intrfc, intrfc.cable) {
				peerId := intrfc.cable.device.devId()
				connectIds(topoGraph, devId, peerId, intrfc.number, intrfc.cable.number)
				connected = true
			}

			if !connected && intrfc.carry != nil && compatibleIntrfcs(intrfc, intrfc.carry) {
				peerId := intrfc.carry.device.devId()
				connectIds(topoGraph, devId, peerId, intrfc.number, intrfc.carry.number)
				connected = true
			}
	
			if !connected && len(intrfc.wireless) > 0 {
				for _, conn := range intrfc.wireless {
					peerId := conn.device.devId()
					connectIds(topoGraph, devId, peerId, intrfc.number, conn.number)
				}
			}
		}
	}
}

// compatibleIntrfcs checks whether the named pair of interfaces are compatible
// w.r.t. their state on cable, carry, and wireless
func compatibleIntrfcs(intrfc1, intrfc2 *intrfcStruct) bool {
	if intrfc1.cable != nil && intrfc2.cable != nil {
		return true
	}
	return intrfc1.carry != nil && intrfc2.carry != nil 
}

// checkConnections checks the graph for full connectivity when the -chkc flag was set

func checkConnections(tg map[int][]int) bool {
	var untouched map[int][]int = make(map[int][]int)

	for srcId, dev := range topoDevById {
		srcType := dev.devType()
		if srcType != endptCode && srcType != filterCode {
			continue
		}	
		for dstId := range topoDevById {
			dstType := dev.devType()
			if dstType != endptCode && dstType != filterCode {
				continue
			}	
			if srcId == dstId {
				continue
			}
			route := findRoute(srcId, dstId)
			if len(*route) == 0 {
				_, present := untouched[srcId]
				if !present {
					untouched[srcId] = []int{}
				}
				untouched[srcId] = append(untouched[srcId], dstId)
			}
		}
	}
	if len(untouched) == 0 {
		return true
	}
	for srcId, missed := range untouched {
		srcDev := topoDevById[srcId]
		srcName := srcDev.devName()
		mlist := []string{}
		for _, dstId := range missed {
			dstDev := topoDevById[dstId]
			mlist = append(mlist, dstDev.devName())
		}
		fmt.Printf("missing paths from %s to %s\n", srcName, strings.Join(mlist,","))
	}
	panic(fmt.Errorf("missing connectivity"))
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
