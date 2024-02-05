package mrnes

import (
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/exp/slices"
	"gopkg.in/yaml.v3"
	"os"
	"path"
	"path/filepath"
	_ "strconv"
	"strings"
)

// A DevExecDesc struct holds a description of a device operation timing.
// ExecTime is the time (in seconds), it depends on attribute Model
type DevExecDesc struct {
	DevOp    string  `json:"devop" yaml:"devop"`
	Model    string  `json:"model" yaml:"model"`
	ExecTime float64 `json:"exectime" yaml:"exectime"`
}

// A DevExecList holds a map (Times) whose key is the operation
// of a device, and whose value is a list of DevExecDescs
// associated with that operation.
type DevExecList struct {
	// ListName is an identifier for this collection of timings
	ListName string `json:"listname" yaml:"listname"`

	// key is the device operation.  Each has a list
	// of descriptions of the timing of that operation, as a function of device model
	Times map[string][]DevExecDesc `json:"times" yaml:"times"`
}

// CreateDevExecList is an initialization constructor.
// Its output struct has methods for integrating data.
func CreateDevExecList(listname string) *DevExecList {
	del := new(DevExecList)
	del.ListName = listname
	del.Times = make(map[string][]DevExecDesc)

	return del
}

// WriteToFile stores the DevExecList struct to the file whose name is given.
// Serialization to json or to yaml is selected based on the extension of this name.
func (del *DevExecList) WriteToFile(filename string) error {
	pathExt := path.Ext(filename)
	var bytes []byte
	var merr error = nil

	if pathExt == ".yaml" || pathExt == ".YAML" || pathExt == ".yml" {
		bytes, merr = yaml.Marshal(*del)
	} else if pathExt == ".json" || pathExt == ".JSON" {
		bytes, merr = json.MarshalIndent(*del, "", "\t")
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

	return werr
}

// ReadDevExecList deserializes a byte slice holding a representation of an DevExecList struct.
// If the input argument of dict (those bytes) is empty, the file whose name is given is read
// to acquire them.  A deserialized representation is returned, or an error if one is generated
// from a file read or the deserialization.
func ReadDevExecList(filename string, useYAML bool, dict []byte) (*DevExecList, error) {
	var err error

	// if the dict slice of bytes is empty we get them from the file whose name is an argument
	if len(dict) == 0 {
		dict, err = os.ReadFile(filename)
		if err != nil {
			return nil, err
		}
	}

	example := DevExecList{}

	if useYAML {
		err = yaml.Unmarshal(dict, &example)
	} else {
		err = json.Unmarshal(dict, &example)
	}

	if err != nil {
		return nil, err
	}

	return &example, nil
}

// AddTiming takes the parameters of a DevExecDesc, creates one, and adds it to the FuncExecList
func (del *DevExecList) AddTiming(devOp, model string, execTime float64) {
	_, present := del.Times[devOp]
	if !present {
		del.Times[devOp] = make([]DevExecDesc, 0)
	}
	del.Times[devOp] = append(del.Times[devOp], DevExecDesc{Model: model, DevOp: devOp, ExecTime: execTime})
}

// numberOfIntrfcs (and more generally, numberOf{Objects}
// are counters of the number of default instances of each
// object type have been created, and so can be used
// to help create unique default names for these objects
//
//	Not currently used at initialization, see if useful for the simulation
var numberOfIntrfcs int = 0
var numberOfNetworks int = 0
var numberOfRouters int = 0
var numberOfSwitches int = 0
var numberOfBrdcstDmns int = 0
var numberOfHosts int = 0

// maps that let you use a name to look up an object
var objTypeByName map[string]string = make(map[string]string)
var devByName map[string]NetDevice = make(map[string]NetDevice)
var netByName map[string]*NetworkFrame = make(map[string]*NetworkFrame)
var rtrByName map[string]*RouterFrame = make(map[string]*RouterFrame)
var BCDbyName map[string]*BroadcastDomainFrame = make(map[string]*BroadcastDomainFrame)

// DevConnected gives for each NetDev device a list of the other NetDev devices
// it connects to through wired interfaces
var DevConnected map[string][]string = make(map[string][]string)

// To most easily serialize and deserialize the various structs involved in creating
// and communicating a simulation model, we ensure that they are all completely
// described without pointers, every structure is fully instantiated in the description.
// On the other hand it is easily to manage the construction of complicated structures
// under the rules Golang uses for memory management if we allow pointers.
// Our approach then is to define two respresentations for each kind of structure.  One
// has the final appellation of 'Frame', and holds pointers.  The pointer free version
// has the final  appellation of 'Desc'.   After completely building the structures using
// Frames we transform each into a Desc version for serialization.

// The NetDevice interface lets us use common code when network objects
// (host, switch, router, network) are involved in model construction.
type NetDevice interface {
	DevName() string                 // returns the .Name field of the struct
	DevId() string                   // returns a unique (string) identifier for the struct
	DevType() string                 // returns the type ("Switch","Router","Host","Network")
	DevInterfaces() []*IntrfcFrame   // list of interfaces attached to the NetDevice, if any
	DevAddIntrfc(*IntrfcFrame) error // function to add another interface to the netDevic3
}

// IntrfcDesc defines a serializable description of a network interface
type IntrfcDesc struct {
	// name for interface, unique among interfaces on hosting device.
	Name string `json:"name" yaml:"name"`

	// type of device that is home to this interface, i.e., "Host", "Switch", "Router"
	DevType string `json:"devtype" yaml:"devtype"`

	// whether media used by interface is 'wired' or 'wireless' .... could put other kinds here, e.g., short-wave, satellite
	MediaType string `json:"mediatype" yaml:"mediatype"`

	// name of host, switch, or router on which this interface is resident
	Device string `json:"device" yaml:"device"`

	// name of interface (on a different device) to which this interface is directly (and singularly) connected
	Connects string `json:"connects" yaml:"connects"`

	// name of the network the interface connects to. There is a tacit assumption then that interface reaches routers on the network
	Faces string `json:"faces" yaml:"faces"`
}

// IntrfcFrame gives a pre-serializable description of an interface, used in model construction.
// 'Almost' the same as IntrfcDesc, with the exception of one pointer
type IntrfcFrame struct {
	// name for interface, unique among interfaces on hosting device.
	Name string

	// type of device that is home to this interface, i.e., "Host", "Switch", "Router"
	DevType string

	// whether media used by interface is 'wired' or 'wireless' .... could put other kinds here, e.g., short-wave, satellite
	MediaType string

	// name of host, switch, or router on which this interface is resident
	Device string

	// pointer to interface (on a different device) to which this interface is directly (and singularly) connected
	Connects *IntrfcFrame

	// name of the network the interface connects to. There is a tacit assumption then that interface reaches routers on the network
	Faces string
}

// DefaultIntrfcName generates a unique string to use as a name for an interface.
// That name includes the name of the device hosting the interface and a counter
func DefaultIntrfcName(device string) string {
	return fmt.Sprintf("intrfc@%s[.%d]", device, numberOfIntrfcs)
}

// ConnectIntrfcFrames links two interfaces through their 'Connects' attributes
func ConnectIntrfcFrames(intrfc1, intrfc2 *IntrfcFrame) {
	intrfc1.Connects = intrfc2
	intrfc2.Connects = intrfc1
}

// CreateIntrfcDesc is a constructor for [IntrfcFrame] that fills in most of the attributes except Connect.
// Arguments name the device holding the interface and its type, the type of communication fabric the interface uses, and the
// name of the network the interface connects to
func CreateIntrfcFrame(device, name, devType, mediaType, faces string) *IntrfcFrame {
	intrfc := new(IntrfcFrame)

	// counter used in the generation of default names
	numberOfIntrfcs += 1

	// an empty string given as name flags that we should create a default one.
	if len(name) == 0 {
		name = DefaultIntrfcName(device)
	}

	// fill in structure attributes included in function call
	intrfc.Device = device
	intrfc.Name = name
	intrfc.DevType = devType
	intrfc.MediaType = mediaType
	intrfc.Faces = faces

	// if the device in which this interface is embedded is not a router we are done
	if devType != "Router" {
		return intrfc
	}

	// embedded in a router. Get its frame and that of the network which is faced
	rtr := devByName[device].(*RouterFrame)
	net := netByName[faces]

	// before adding the router to the network's list of routers, check for duplication
	// (based on the router's name)
	duplicated := false
	for _, stored := range net.Routers {
		if rtr.Name == stored.Name {
			duplicated = true

			break
		}
	}

	// OK to save
	if !duplicated {
		net.Routers = append(net.Routers, rtr)
	}

	return intrfc
}

// Transform converts an IntrfcFrame and returns an IntrfcDesc, for serialization.
func (ifs *IntrfcFrame) Transform() IntrfcDesc {
	// most attributes are stright copies
	intrfcDesc := new(IntrfcDesc)
	intrfcDesc.Device = ifs.Device
	intrfcDesc.Name = ifs.Name
	intrfcDesc.DevType = ifs.DevType
	intrfcDesc.MediaType = ifs.MediaType
	intrfcDesc.Faces = ifs.Faces

	// a IntrfcDesc defines its Connects field to be a string, which
	// we set here to be the name of the device the IntrfcFrame version
	// points to
	if ifs.Connects != nil {
		intrfcDesc.Connects = ifs.Connects.Name
	}
	return *intrfcDesc
}

// IsConnected is part of a set of functions and data structures useful in managing
// construction of a communication network. It indicates whether two devices whose
// identities are given are already connected through their interfaces.
func IsConnected(id1, id2 string) bool {
	_, present := DevConnected[id1]
	if !present {
		return false
	}
	for _, peerId := range DevConnected[id1] {
		if peerId == id2 {
			return true
		}
	}

	return false
}

// MarkConnected modifes the DevConnected data structure to reflect that
// the devices whose identities are the arguments have been connected.
func MarkConnected(id1, id2 string) {
	// if already connected there is nothing to do here
	if IsConnected(id1, id2) {
		return
	}

	// for both devices, add their names to the 'connected to' list of the other

	// complete the data structure for DevConnected[id1][id2] if need be
	_, present := DevConnected[id1]
	if !present {
		DevConnected[id1] = []string{}
	}

	DevConnected[id1] = append(DevConnected[id1], id2)

	// complete the data structure for DevConnected[id2][id1] if need be
	_, present = DevConnected[id2]
	if !present {
		DevConnected[id2] = []string{}
	}
	DevConnected[id2] = append(DevConnected[id2], id1)
}

// ConnectDevs establishes a connection (creating interfaces if needed) between
// devices dev1 and dev2 (recall that NetDevice is an interface satisified by Host, Router, Switch)
func ConnectDevs(dev1, dev2 NetDevice, faces string) error {
	errList := []error{}
	// if already connected we don't do anything
	if IsConnected(dev1.DevId(), dev2.DevId()) {
		return nil
	}

	// this call will record the connection
	MarkConnected(dev1.DevId(), dev2.DevId())

	// get local copies of the interfaces for both devices
	intrfcs1 := dev1.DevInterfaces()
	intrfcs2 := dev2.DevInterfaces()

	// get local copies of the device names
	name1 := dev1.DevName()
	name2 := dev2.DevName()

	type1 := dev1.DevType()
	type2 := dev2.DevType()

	// determine interface media type.  Default is "wired"
	mediaType := "wired"

	// Switch interfaces are always wireline, and hosts always connect solely to the hub of their BCD.
	// If a BCD is wireless its hub will be a router, so we can infer a wireless connection if we're connecting a host and a Router
	if (type1 == "Host" && type2 == "Router") || (type2 == "Host" && type1 == "Router") {
		mediaType = "wireless"
	}

	// the nature of a router-to-router connection depends on whether one of the routers is a wireless hub
	if type1 == "Router" && type2 == "Router" {
		rtr1 := rtrByName[name1]
		rtr2 := rtrByName[name2]

		// if either router is a hub for a broadcast domain the connection is wireless,
		// and faces becomes the name of the broadcast domain network
		if len(rtr1.BrdcstDmn) > 0 || len(rtr2.BrdcstDmn) > 0 {
			mediaType = "wireless"

			// get the broadcast domain name from which router is the hub
			brdName := rtr1.BrdcstDmn
			if len(brdName) == 0 {
				brdName = rtr2.BrdcstDmn
			}

			// get a pointer to the broadcast domain frame
			brdcstdmn := BCDbyName[brdName]

			// get the name of the network encapsulating the BCD
			faces = brdcstdmn.Network
		}
	} else {
		// connection media is whatever the network they share is
		mediaType = netByName[faces].MediaType
	}

	// for every interface on dev1 examine the Connects
	// value for a match with dev2. Finding a match, save the name of the network they both face
	marked := false
	if mediaType == "wired" {
		for _, intrfc := range intrfcs1 {
			if intrfc.MediaType == "wired" && intrfc.Connects != nil && intrfc.Connects.Device == dev2.DevName() {
				intrfc.Faces = faces
				intrfc.Connects.Faces = faces
				marked = true
			}
		}
	}

	// do the same for dev2.  One would expect by symmetry that if an interface on dev1 connects to an interface
	// on dev2 then that same interface on dev2 connects to that same interface on dev1.   ISTM that if a user
	// is in complete control of model construction (without using utility programs provided here), that assumption
	// may not hold, so the logic below is insurance
	for _, intrfc := range intrfcs2 {
		if mediaType == "wired" {
			if intrfc.MediaType == "wired" && intrfc.Connects != nil && intrfc.Connects.Device == dev1.DevName() {
				intrfc.Faces = faces
				intrfc.Connects.Faces = faces
				marked = true
			}
		}
	}

	// marked as needed, we can go now
	if marked {
		return nil
	}

	// if one of the devices is a Host and the other is a router, we need to create
	// a wireless interface for the router if one does not already exist.
	wirelessHost := (type1 == "Host" && type2 == "Router") || (type2 == "Host" && type1 == "Router")

	if wirelessHost {
		// see whether the router hub already has a wireless interface
		var hostIntrfc *IntrfcFrame
		var rtrIntrfc *IntrfcFrame

		// initialize these
		hostIntrfcList := intrfcs1
		rtrIntrfcList := intrfcs2
		hostName := name1
		rtrName := name2
		hostDev := dev1
		rtrDev := dev2

		// change the assignments around if device 1 is the host
		if type1 == "Host" {
			hostIntrfcList = intrfcs2
			rtrIntrfcList = intrfcs1
			hostDev = dev2
			rtrDev = dev1
			hostName = name2
			rtrName = name1
		}

		found := false
		// set found to true if we find a wireless interface on this router
		for _, intrfc := range rtrIntrfcList {
			if intrfc.MediaType == "wireless" {
				found = true

				break
			}
		}

		// not finding a wireless interface on the hub we will create one
		if !found {
			// add a wireless interface. The hub name is in name1, we ask for a default interface name to be created,
			// the network fabric is wireless and the network faced by the interface was provided as a function input argument
			rtrIntrfc = CreateIntrfcFrame(rtrName, "", rtrDev.DevType(), "wireless", faces)
			err := rtrDev.DevAddIntrfc(rtrIntrfc)
			errList = append(errList, err)
		}

		// dev2 is a Host which may have multiple interfaces, with multiple ones of them being wireless.
		// So here we look for an existing wireless interface that points to the network of interest.

		found = false
		// set found to true if we find a wireless interface on this host that faces the network identified in the func input argument
		for _, intrfc := range hostIntrfcList {
			if intrfc.MediaType == "wireless" && intrfc.Faces == faces {
				found = true

				break
			}
		}

		// not finding a wireless interface on the hub we will create one
		if !found {
			// add a wireless interface. The host name is in name2, we ask for a default interface name to be created,
			// the network fabric is wireless and the network faced by the interface was provided as a function input argument
			hostIntrfc = CreateIntrfcFrame(hostName, "", hostDev.DevType(), "wireless", faces)
			err := hostDev.DevAddIntrfc(hostIntrfc)
			errList = append(errList, err)
		}

		return ReportErrs(errList)
	}

	// connection to be made is wired.

	// create an interface for dev1
	name := DefaultIntrfcName(name1)
	intrfc1 := CreateIntrfcFrame(name1, name, dev1.DevType(), "wired", faces)

	// create an interface for dev2
	name = DefaultIntrfcName(name2)
	intrfc2 := CreateIntrfcFrame(name2, name, dev2.DevType(), "wired", faces)

	// connect the new interfaces
	ConnectIntrfcFrames(intrfc1, intrfc2)

	// add the interfaces to their devices
	err1 := dev1.DevAddIntrfc(intrfc1)
	err2 := dev2.DevAddIntrfc(intrfc2)
	errList = append(errList, err1, err2)

	return ReportErrs(errList)
}

// A NetworkFrame holds the attributes of a network during the model construction phase
type NetworkFrame struct {
	// Name is a unique name across all objects in the simulation. It is used universally to reference this network
	Name string

	// NetType describes role of network, e.g., LAN, WAN, T3, T2, T1.  Used as an attribute when doing experimental configuration
	NetType string

	// for now the network is either "wired" or "wireless"
	MediaType string

	// BCD's that are nestled into this network
	BrdcstDmns []*BroadcastDomainFrame

	// any router with an interface that faces this network is in this list
	Routers []*RouterFrame
}

// NetworkDesc is a serializable version of the Network information, where
// the pointers to broadcast domains and routers are replaced by the string
// names of those entities
type NetworkDesc struct {
	Name       string   `json:"name" yaml:"name"`
	NetType    string   `json:"nettype" yaml:"nettype"`
	MediaType  string   `json:"mediatype" yaml:"mediatype"`
	BrdcstDmns []string `json:"brdcstdmns" yaml:"brdcstdmns"`
	Routers    []string `json:"routers" yaml:"routers"`
}

// CreateNetworkFrame is a constructor, with all the inherent attributes specified
func CreateNetworkFrame(name, NetType string, MediaType string) *NetworkFrame {
	nf := new(NetworkFrame)
	nf.Name = name           // name that is unique across entire simulation model
	nf.NetType = NetType     // type such as "LAN", "WAN", "T3", "T2", "T1"
	nf.MediaType = MediaType // currently "wired" or "wireless"

	// initialize slices
	nf.BrdcstDmns = make([]*BroadcastDomainFrame, 0)
	nf.Routers = make([]*RouterFrame, 0)

	objTypeByName[name] = "Network" // object name gets you object type
	netByName[name] = nf            // network name gets you network frame
	numberOfNetworks += 1           // one more network

	return nf
}

// AddBrdcstDmn includes the argument broadcast domain into the network,
// throws an error if already present
func (nf *NetworkFrame) AddBrdcstDmn(bcdf *BroadcastDomainFrame) error {
	// complain if a broadcast domain with this same name already exists here
	for _, bcd := range nf.BrdcstDmns {
		if bcd.Name == bcdf.Name {
			fmt.Printf("attempt to add broadcast domain %s multiple times to network %s\n", bcdf.Name, nf.Name)

			return fmt.Errorf("attempt to add broadcast domain %s multiple times to network %s\n", bcdf.Name, nf.Name)
		}
	}

	// bcdf is unique, save it
	nf.BrdcstDmns = append(nf.BrdcstDmns, bcdf)

	return nil
}

// AddRouter includes the argument router into the network,
// throws an error if already present
func (nf *NetworkFrame) AddRouter(rtrf *RouterFrame) error {
	// check whether a router with this same name already exists here
	for _, rtr := range nf.Routers {
		if rtr.Name == rtrf.Name {
			fmt.Printf("attempt to add router %s multiple times to network %s\n", rtrf.Name, nf.Name)
			return fmt.Errorf("attempt to add router %s multiple times to network %s\n", rtrf.Name, nf.Name)
		}
	}
	nf.Routers = append(nf.Routers, rtrf)

	return nil
}

// Transform converts a network frame into a network description.
// It copies string attributes, and converts pointers to broadcast domains and routers
// to strings with the names of those entities
func (nf *NetworkFrame) Transform() NetworkDesc {
	nd := new(NetworkDesc)
	nd.Name = nf.Name
	nd.NetType = nf.NetType
	nd.MediaType = nf.MediaType

	// in the frame the BCDs are pointers to objects, now we store their names
	nd.BrdcstDmns = make([]string, len(nf.BrdcstDmns))
	for idx := 0; idx < len(nf.BrdcstDmns); idx += 1 {
		nd.BrdcstDmns[idx] = nf.BrdcstDmns[idx].Name
	}

	// in the frame the routers are poointer to objects, now we store their names
	nd.Routers = make([]string, len(nf.Routers))
	for idx := 0; idx < len(nf.Routers); idx += 1 {
		nd.Routers[idx] = nf.Routers[idx].Name
	}

	return *nd
}

// RouterDesc describes parameters of a Router in the topology.
type RouterDesc struct {
	// Name is unique string identifier used to reference the router
	Name string `json:"name" yaml:"name"`

	// Model is an attribute like "Cisco 6400". Used primarily in run-time configuration
	Model string `json:"model" yaml:"model"`

	// name of broadcast domain	this router serves as hub.  Empty if it does not
	BrdcstDmn string `json:"brdcstdmn" yaml:"brdcstdmn"`

	// list of names interfaces that describe the ports of the router
	Interfaces []IntrfcDesc `json:"interfaces" yaml:"interfaces"`
}

// RouterDesc describes parameters of a Router in the topology in pre-serialized form
type RouterFrame struct {
	Name       string         // identitical to RouterDesc attribute
	Model      string         // identifical to RouterDesc attribute
	BrdcstDmn  string         // name of broadcast domain	this router serves as hub.  Empty if it does not
	Interfaces []*IntrfcFrame // list of interface frames that describe the ports of the router
}

// DefaultRouterName returns a unique name for a router
func DefaultRouterName() string {
	return fmt.Sprintf("rtr.[%d]", numberOfRouters)
}

// CreateRouterFrame is a constructor, stores (possibly creates default) name, initializes slice of interface frames
func CreateRouterFrame(name, brdcstDmn string) *RouterFrame {
	rf := new(RouterFrame)
	numberOfRouters += 1

	rf.Model = "cisco"

	rf.BrdcstDmn = brdcstDmn

	if len(name) == 0 {
		name = DefaultRouterName()
	}

	rf.Name = name
	objTypeByName[name] = "Router"
	devByName[name] = rf
	rtrByName[name] = rf
	rf.Interfaces = make([]*IntrfcFrame, 0)

	return rf
}

// DevName returns the name of the NetDevice
func (rf *RouterFrame) DevName() string {
	return rf.Name
}

// devType returns network objec type (e.g., "Switch", "Router", "Host", "Network") for the NetDevice
func (rf *RouterFrame) DevType() string {
	return "Router"
}

// devId returns a unique identifier for the NetDevice
func (rf *RouterFrame) DevId() string {
	return rf.Name
}

// devModel returns the NetDevice model code, if any
func (rf *RouterFrame) DevModel() string {
	return rf.Model
}

// devInterfaces returns the slice of IntrfcFrame held by the NetDevice, if any
func (rf *RouterFrame) DevInterfaces() []*IntrfcFrame {
	return rf.Interfaces
}

// AddIntrfc includes interface frame in router frame
func (rf *RouterFrame) AddIntrfc(intrfc *IntrfcFrame) error {
	for _, ih := range rf.Interfaces {
		if ih == intrfc || ih.Name == intrfc.Name {
			return fmt.Errorf("attempt to re-add interface %s to switch %s\n", intrfc.Name, rf.Name)
		}
	}

	// ensure that the interface has stored the home device type and name
	intrfc.Device = rf.Name
	intrfc.DevType = "Switch"
	rf.Interfaces = append(rf.Interfaces, intrfc)

	return nil
}

// devAddIntrfc includes an IntrfcFrame to the NetDevice
func (rf *RouterFrame) DevAddIntrfc(iff *IntrfcFrame) error {
	return rf.AddIntrfc(iff)
}

// Transform returns a serializable RouterDesc, transformed from a RouterFrame.
func (rdf *RouterFrame) Transform() RouterDesc {
	rd := new(RouterDesc)
	rd.Name = rdf.Name
	rd.Model = rdf.Model
	rd.BrdcstDmn = rdf.BrdcstDmn

	// create serializable representation of the interfaces by calling the Transform method on their Frame representation
	rd.Interfaces = make([]IntrfcDesc, len(rdf.Interfaces))
	for idx := 0; idx < len(rdf.Interfaces); idx += 1 {
		rd.Interfaces[idx] = rdf.Interfaces[idx].Transform()
	}

	return *rd
}

// SwitchDesc holds a serializable representation of a switch.
type SwitchDesc struct {
	Name       string       `json:"name" yaml:"name"`
	BrdcstDmn  string       `json:"brdcstdmn" yaml:"brdcstdmn"`
	Model      string       `json:"model" yaml:"model"`
	Interfaces []IntrfcDesc `json:"interfaces" yaml:"interfaces"`
}

// SwitchFrame holds a pre-serialization representation of a Switch
type SwitchFrame struct {
	Name       string         // unique string identifier used to reference the router
	BrdcstDmn  string         // name of broadcast domain switch lives in
	Model      string         // device model identifier
	Interfaces []*IntrfcFrame // interface frames that describe the ports of the router
}

// DefaultSwitchName returns a unique name for a switch
func DefaultSwitchName(name string) string {
	return fmt.Sprintf("switch(%s).%d", name, numberOfSwitches)
}

// CreateSwitchFrame constructs a switch frame.  Saves (and possibly creates) the switch name,
// saves the name of the broadcast domain in which the switch lives
func CreateSwitchFrame(bcd, name string) *SwitchFrame {
	sf := new(SwitchFrame)
	numberOfSwitches += 1

	sf.BrdcstDmn = bcd

	if len(name) == 0 {
		name = DefaultSwitchName(bcd)
	}
	objTypeByName[name] = "Switch" // from the name look up the type of object
	devByName[name] = sf           // from the name look up the device

	sf.Name = name
	sf.Interfaces = make([]*IntrfcFrame, 0) // initialize for additions

	return sf
}

// AddIntrfc includes a new interface frame for the switch.  Error is returned
// if the interface (or one with the same name) is already attached to the SwitchFrame
func (sf *SwitchFrame) AddIntrfc(iff *IntrfcFrame) error {
	// check whether interface exists here already
	for _, ih := range sf.Interfaces {
		if ih == iff || ih.Name == iff.Name {
			return fmt.Errorf("attempt to re-add interface %s to switch %s\n", iff.Name, sf.Name)
		}
	}

	// ensure that the interface has stored the home device type and name
	iff.Device = sf.Name
	iff.DevType = "Switch"
	sf.Interfaces = append(sf.Interfaces, iff)

	return nil
}

// DevName returns name for the NetDevice
func (sf *SwitchFrame) DevName() string {
	return sf.Name
}

// devType returns the type of the NetDevice (e.g. "Switch","Router","Host","Network")
func (sf *SwitchFrame) DevType() string {
	return "Switch"
}

// devId returns unique identifier for NetDevice
func (sf *SwitchFrame) DevId() string {
	return sf.Name
}

// devInterfaces returns list of IntrfcFrames attached to the NetDevice, if any
func (sf *SwitchFrame) DevInterfaces() []*IntrfcFrame {
	return sf.Interfaces
}

// devAddIntrfc adds an IntrfcFrame to the NetDevice
func (sf *SwitchFrame) DevAddIntrfc(iff *IntrfcFrame) error {
	return sf.AddIntrfc(iff)
}

// Transform returns a serializable SwitchDesc, transformed from a SwitchFrame.
func (sf *SwitchFrame) Transform() SwitchDesc {
	sd := new(SwitchDesc)
	sd.Name = sf.Name
	sd.Model = sf.Model
	sd.BrdcstDmn = sf.BrdcstDmn

	// serialize the interfaces by calling their own serialization routines
	sd.Interfaces = make([]IntrfcDesc, len(sf.Interfaces))
	for idx := 0; idx < len(sf.Interfaces); idx += 1 {
		sd.Interfaces[idx] = sf.Interfaces[idx].Transform()
	}

	return *sd
}

// HostDesc defines serializable representation of a Host.
type HostDesc struct {
	Name       string       `json:"name" yaml:"name"`
	BrdcstDmn  []string     `json:"brdcstdmn" yaml:"brdcstdmn"`
	HostType   string       `json:"hosttype" yaml:"hosttype"`
	Model      string       `json:"model" yaml:"model"`
	Network    string       `json:"network" yaml:"network"`
	Interfaces []IntrfcDesc `json:"interfaces" yaml:"interfaces"`
}

// HostFrame defines pre-serialization representation of a Host
type HostFrame struct {
	Name       string         // unique string identifier
	HostType   string         // parameter used to index into execution time tables
	BrdcstDmn  []string       // broadcast domain(s) host is connected to
	Network    string         // name of network containing host
	Interfaces []*IntrfcFrame // list of interfaces that describe the networks the host connects to
}

// DefaultHostName returns unique name for a host
func DefaultHostName(name string) string {
	return fmt.Sprintf("host(%s).(%d)", name, numberOfHosts)
}

// CreateHostFrame is a constructor. It saves (or creates) the host name, and saves
// the optional host type (which has use in run-time configuration)
func CreateHostFrame(name, hostType string) *HostFrame {
	hf := new(HostFrame)
	numberOfHosts += 1

	hf.HostType = hostType

	// get a (presumeably unique) string name
	if len(name) == 0 {
		name = DefaultHostName(name)
	}
	hf.Name = name
	objTypeByName[name] = "Host" // from name get type of object, here, "Host"
	devByName[name] = hf         // from name get object

	hf.Interfaces = make([]*IntrfcFrame, 0) // initialize slice of interface frames

	hf.BrdcstDmn = make([]string, 0) // initialize slice of BCDs host is in

	return hf
}

// Transform returns a serializable HostDesc, transformed from a HostFrame.
func (hf *HostFrame) Transform() HostDesc {
	hd := new(HostDesc)
	hd.Name = hf.Name
	hd.HostType = hf.HostType
	hd.Network = hf.Network
	hd.BrdcstDmn = hf.BrdcstDmn

	// serialize the interfaces by calling the interface transformation function
	hd.Interfaces = make([]IntrfcDesc, len(hf.Interfaces))
	for idx := 0; idx < len(hf.Interfaces); idx += 1 {
		hd.Interfaces[idx] = hf.Interfaces[idx].Transform()
	}

	return *hd
}

// AddIntrfc includes a new interface frame for the host.
// An error is reported if this specific (by pointer value or by name) interface is already connected.
func (hf *HostFrame) AddIntrfc(iff *IntrfcFrame) error {
	for _, ih := range hf.Interfaces {
		if ih == iff || ih.Name == iff.Name {
			return fmt.Errorf("attempt to re-add interface %s to switch %s\n", iff.Name, hf.Name)
		}
	}

	// ensure that interface states its presence on this device
	iff.DevType = "Host"
	iff.Device = hf.Name

	// save the interface
	hf.Interfaces = append(hf.Interfaces, iff)

	return nil
}

// DevName returns the NetDevice name
func (sf *HostFrame) DevName() string {
	return sf.Name
}

// devId returns the NetDevice unique identifier
func (sf *HostFrame) DevId() string {
	return sf.Name
}

// devType returns the NetDevice Type
func (sf *HostFrame) DevType() string {
	return "Host"
}

// devInterfaces returns the NetDevice list of IntrfcFrames, if any
func (sf *HostFrame) DevInterfaces() []*IntrfcFrame {
	return sf.Interfaces
}

// devAddIntrfc includes an IntrfcFrame to a NetDevice's list of IntrfcFrames
func (hf *HostFrame) DevAddIntrfc(iff *IntrfcFrame) error {
	return hf.AddIntrfc(iff)
}

// BrdcstDomainDesc describes a serializable representation of a broadcast domain,
type BroadcastDomainDesc struct {
	Name      string `json:"name" yaml:"name"`
	Network   string `json:"network" yaml:"network"`
	MediaType string `json:"mediatype" yaml:"mediatype"`

	//slice of names of hosts (relative to broadcast domain) in the same BD
	Hosts []string `json:"hosts" yaml:"hosts"`

	// name of device that connects hosts and routers in the BD.  Maybe either a switch or a router
	Hub string `json:"hub" yaml:"hub"`
}

// BrdcstDomain describes a pre-serializable representation of a broadcast domain.
type BroadcastDomainFrame struct {
	Name      string       // unique name
	Network   string       // name of network containing BCD
	MediaType string       // currently "wired" or "wireless"
	Hosts     []*HostFrame // list of frames of Hosts with interfaces that connect to the BCD hub

	// exactly one of these hubs may be used (is non-nil)
	SwitchHub *SwitchFrame
	RouterHub *RouterFrame
}

// return a default name for a broadcast domain
func DefaultBrdcstDmnName(netName string) string {
	return fmt.Sprintf("bcd(%s).[%d]", netName, numberOfBrdcstDmns)
}

// CreateBroadcastDomainFrame is a constructor.
func CreateBroadcastDomainFrame(network, name, mediaType string) (*BroadcastDomainFrame, error) {
	bcdf := new(BroadcastDomainFrame)
	numberOfBrdcstDmns += 1

	if len(name) == 0 { // empty input name is request for generating a default one
		name = DefaultBrdcstDmnName(network)
	}

	bcdf.Name = name
	bcdf.Network = network
	bcdf.MediaType = mediaType
	bcdf.Hosts = []*HostFrame{}

	BCDbyName[name] = bcdf

	objTypeByName[name] = "BrdcstDmn"

	var intrfcErr error
	// if the broadcast domain is wired it has a switch.  If not it has a router
	if mediaType == "wired" {
		bcdf.SwitchHub = CreateSwitchFrame(name, "")
	} else if mediaType == "wireless" {
		rtr := CreateRouterFrame(name+".Hub", name)
		bcdf.RouterHub = rtr

		// create a wireless interface for it that faces the network defined by the BCD
		intrfc := CreateIntrfcFrame(rtr.Name, "", "Router", "wireless", network)
		intrfcErr = rtr.AddIntrfc(intrfc)
	}

	return bcdf, intrfcErr
}

func (bcdf *BroadcastDomainFrame) AddHost(host *HostFrame) {
	for _, hst := range bcdf.Hosts {
		if hst == host || hst.Name == host.Name {
			return
		}
	}
	bcdf.Hosts = append(bcdf.Hosts, host)

	// include the broadcast domain name on the host
	host.BrdcstDmn = append(host.BrdcstDmn, bcdf.Name)

	// if the BCD is wired connect the host to the hub
	if bcdf.MediaType == "wired" {
		err := ConnectDevs(bcdf.SwitchHub, host, bcdf.Network)
		if err != nil {
			panic(err)
		}
	} else {
		err := ConnectDevs(bcdf.RouterHub, host, bcdf.Network)
		if err != nil {
			panic(err)
		}
	}
}

// Transform returns a serializable BroadcastDomainDesc from a pre-serializable
// BroadcastDomainFrame.
func (bcdf *BroadcastDomainFrame) Transform() BroadcastDomainDesc {
	bcd := new(BroadcastDomainDesc)

	// copy the string variables that are identical
	bcd.Name = bcdf.Name
	bcd.Network = bcdf.Network
	bcd.MediaType = bcdf.MediaType

	// select Hub based on media type
	if bcd.MediaType == "wired" {
		bcd.Hub = bcdf.SwitchHub.Name
	} else if bcd.MediaType == "wireless" {
		bcd.Hub = bcdf.RouterHub.Name
	}

	// replace pointers to hosts with the 'Name' value of the host
	bcd.Hosts = make([]string, len(bcdf.Hosts))
	for idx := 0; idx < len(bcdf.Hosts); idx += 1 {
		bcd.Hosts[idx] = bcdf.Hosts[idx].Name
	}

	return *bcd
}

// ConnectNetworks creates router that enables traffic to pass between
// the two argument networks. 'newRtr' input variable governs whether
// a new router is absolutely created (allowing for multiple connections),
// or only if there is no existing connection
func ConnectNetworks(net1, net2 *NetworkFrame, newRtr bool) (*RouterFrame, error) {
	// count the number of routers that net1 and net2 share already
	var shared int = 0
	for _, rtr1 := range net1.Routers {
		for _, rtr2 := range net2.Routers {
			if rtr1.Name == rtr2.Name {
				shared += 1
			}
		}
	}

	// if one or more is shared already and newRtr is false, just return nil
	if shared > 0 && !newRtr {
		return nil, nil
	}

	// create a router that has one interface towards net1 and the other towards net1
	name := "Rtr:(" + net1.Name + "-" + net2.Name + ")"
	if net2.Name < net1.Name {
		name = "Rtr:(" + net2.Name + "-" + net1.Name + ")"
	}

	// append shared+1 to ensure no duplication in router names
	name = fmt.Sprintf("%s.[%d]", name, shared+1)
	rtr := CreateRouterFrame(name, "")

	// create an interface bound to rtr that faces net1
	intrfc1 := CreateIntrfcFrame(rtr.Name, "", "Router", net1.MediaType, net1.Name)
	intrfc1Err := rtr.AddIntrfc(intrfc1)

	// create an interface bound to rtr that faces net2
	intrfc2 := CreateIntrfcFrame(rtr.Name, "", "Router", net2.MediaType, net2.Name)
	intrfc2Err := rtr.AddIntrfc(intrfc2)

	return rtr, ReportErrs([]error{intrfc1Err, intrfc2Err})
}

// The TopoCfgFrame struc gives the highest level structure of the topology,
// is ultimately the encompassing dictionary in the serialization
type TopoCfgFrame struct {
	Name             string
	Hosts            []*HostFrame
	Networks         []*NetworkFrame
	Routers          []*RouterFrame
	Switches         []*SwitchFrame
	BroadcastDomains []*BroadcastDomainFrame
}

// CreateTopoCfgFrame is a constructor.
func CreateTopoCfgFrame(name string) TopoCfgFrame {
	TF := new(TopoCfgFrame)
	TF.Name = name // save name

	// initialize all the TopoCfgFrame slices
	TF.Hosts = make([]*HostFrame, 0)
	TF.Networks = make([]*NetworkFrame, 0)
	TF.Routers = make([]*RouterFrame, 0)
	TF.Switches = make([]*SwitchFrame, 0)
	TF.BroadcastDomains = make([]*BroadcastDomainFrame, 0)

	return *TF
}

// AddHost adds a Host to the topology configuration (if it is not already present)
func (tf *TopoCfgFrame) AddHost(host *HostFrame) {
	// test for duplicatation either by address or by name
	inputName := host.Name
	for _, stored := range tf.Hosts {
		if host == stored || inputName == stored.Name {
			return
		}
	}
	// add it
	tf.Hosts = append(tf.Hosts, host)
}

// AddNetwork adds a Network to the topology configuration (if it is not already present)
func (tf *TopoCfgFrame) AddNetwork(net *NetworkFrame) {
	// test for duplicatation either by address or by name
	inputName := net.Name
	for _, stored := range tf.Networks {
		if net == stored || inputName == stored.Name {
			return
		}
	}
	// add it
	tf.Networks = append(tf.Networks, net)
}

// AddRouter adds a Router to the topology configuration (if it is not already present)
func (tf *TopoCfgFrame) AddRouter(rtr *RouterFrame) {
	// ignore if router is already present. Comparison by address or by name
	inputName := rtr.Name
	for _, stored := range tf.Routers {
		if rtr == stored || inputName == stored.Name {
			return
		}
	}
	// add it
	tf.Routers = append(tf.Routers, rtr)
}

// AddSwitch adds a Switch to the topology configuration (if it is not already present)
func (tf *TopoCfgFrame) AddSwitch(swtch *SwitchFrame) {
	// ignore if switch is already present. Comparison by address or by name
	inputName := swtch.Name
	for _, stored := range tf.Switches {
		if swtch == stored || inputName == stored.Name {
			return
		}
	}
	// add it
	tf.Switches = append(tf.Switches, swtch)
}

// AddBrdcstDmn adds a BroadcastDomain to the topology configuration (if it is not already present)
func (tf *TopoCfgFrame) AddBrdcstDmn(bcd *BroadcastDomainFrame) {
	// ignore if BCD is already present. Comparison by address or by name
	inputName := bcd.Name
	for _, stored := range tf.BroadcastDomains {
		if bcd == stored || inputName == stored.Name {
			return
		}
	}
	// add it
	tf.BroadcastDomains = append(tf.BroadcastDomains, bcd)
}

// Consolidate gathers hosts, switches, and routers from the networks added to the TopoCfgFrame,
// and make sure that all the devices referred to in the different components are exposed
// at the TopoCfgFrame level
func (tcf *TopoCfgFrame) Consolidate() error {
	if len(tcf.Networks) == 0 {
		return fmt.Errorf("No networks given in TopoCfgFrame in Consolidate call\n")
	}

	tcf.Hosts = []*HostFrame{}
	tcf.Routers = []*RouterFrame{}
	tcf.Switches = []*SwitchFrame{}
	tcf.BroadcastDomains = []*BroadcastDomainFrame{}

	for _, net := range tcf.Networks {

		// ensure the connections between switches and routers in the network
		// net.Consolidate()

		for _, bcd := range net.BrdcstDmns {
			if bcd.MediaType == "wired" {
				tcf.AddSwitch(bcd.SwitchHub)
			} else if bcd.MediaType == "wireless" {
				tcf.AddRouter(bcd.RouterHub)
			}
			for _, host := range bcd.Hosts {
				tcf.AddHost(host)
			}
			tcf.AddBrdcstDmn(bcd)
		}
		for _, rtr := range net.Routers {
			tcf.AddRouter(rtr)
		}
	}

	return nil
}

// Transform transforms the slices of pointers to network objects
// into slices of instances of those objects, for serialization
func (tf *TopoCfgFrame) Transform() TopoCfg {
	// first ensure that the TopoCfgFrame is consolidated
	cerr := tf.Consolidate()
	if cerr != nil {
		panic(cerr)
	}

	// create the TopoCfg
	TD := new(TopoCfg)
	TD.Name = tf.Name

	TD.Hosts = make([]HostDesc, 0)
	for _, hostf := range tf.Hosts {
		host := hostf.Transform()
		TD.Hosts = append(TD.Hosts, host)
	}

	TD.Networks = make([]NetworkDesc, 0)
	for _, netf := range tf.Networks {
		net := netf.Transform()
		TD.Networks = append(TD.Networks, net)
	}

	TD.Routers = make([]RouterDesc, 0)
	for _, rtrf := range tf.Routers {
		rtr := rtrf.Transform()
		TD.Routers = append(TD.Routers, rtr)
	}

	TD.BroadcastDomains = make([]BroadcastDomainDesc, 0)
	for _, bcdf := range tf.BroadcastDomains {
		bcd := bcdf.Transform()
		TD.BroadcastDomains = append(TD.BroadcastDomains, bcd)
	}

	TD.Switches = make([]SwitchDesc, 0)
	for _, switchf := range tf.Switches {
		swtch := switchf.Transform()
		TD.Switches = append(TD.Switches, swtch)
	}

	return *TD
}

// Type definitions for TopoCfg attributes
type RtrDescSlice []RouterDesc
type HostDescSlice []HostDesc
type NetworkDescSlice []NetworkDesc
type SwitchDescSlice []SwitchDesc
type BroadcastDomainDescSlice []BroadcastDomainDesc

// TopoCfg contains all of the networks, routers, and
// hosts, as they are listed in the json file.
type TopoCfg struct {
	Name             string                   `json:"name" yaml:"name"`
	Networks         NetworkDescSlice         `json:"networks" yaml:"networks"`
	Routers          RtrDescSlice             `json:"routers" yaml:"routers"`
	Hosts            HostDescSlice            `json:"hosts" yaml:"hosts"`
	Switches         SwitchDescSlice          `json:"switches" yaml:"switches"`
	BroadcastDomains BroadcastDomainDescSlice `json:"brdcstdmns" yaml:"brdcstdmns"`
}

// A TopoCfgDict holds instances of TopoCfg structures, in a map whose key is
// a name for the topology.  Used to store pre-built instances of networks
type TopoCfgDict struct {
	DictName string             `json:"dictname" yaml:"dictname"`
	Cfgs     map[string]TopoCfg `json:"cfgs" yaml:"cfgs"`
}

// CreateTopoCfgDict is a constructor. Saves the dictionary name, initializes the TopoCfg map.
func CreateTopoCfgDict(name string) *TopoCfgDict {
	tcd := new(TopoCfgDict)
	tcd.DictName = name
	tcd.Cfgs = make(map[string]TopoCfg)

	return tcd
}

// AddTopoCfg includes a TopoCfg into the dictionary, optionally returning an error
// if an TopoCfg with the same name has already been included
func (tcd *TopoCfgDict) AddTopoCfg(tc *TopoCfg, overwrite bool) error {
	if !overwrite {
		_, present := tcd.Cfgs[tc.Name]
		if present {
			return fmt.Errorf("attempt to overwrite TopoCfg %s in TopoCfgDict", tc.Name)
		}
	}

	tcd.Cfgs[tc.Name] = *tc

	return nil
}

// RecoverTopoCfg returns a copy (if one exists) of the TopoCfg with name equal to the input argument name.
// Returns a boolean indicating whether the entry was actually found
func (tcd *TopoCfgDict) RecoverTopoCfg(name string) (*TopoCfg, bool) {
	tc, present := tcd.Cfgs[name]
	if present {
		return &tc, true
	}

	return nil, false
}

// WriteToFile serializes the TopoCfgDict and writes to the file whose name is given as an input argument.
// Extension of the file name selects whether serialization is to json or to yaml format.
func (tcd *TopoCfgDict) WriteToFile(filename string) error {
	pathExt := path.Ext(filename)
	var bytes []byte
	var merr error

	if pathExt == ".yaml" || pathExt == ".YAML" || pathExt == ".yml" {
		bytes, merr = yaml.Marshal(*tcd)
	} else if pathExt == ".json" || pathExt == ".JSON" {
		bytes, merr = json.MarshalIndent(*tcd, "", "\t")
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

	return werr
}

// ReadTopoCfgDict deserializes a slice of bytes into a TopoCfgDict.  If the input arg of bytes
// is empty, the file whose name is given as an argument is read.  Error returned if
// any part of the process generates the error.
func ReadTopoCfgDict(topoCfgDictFileName string, useYAML bool, dict []byte) (*TopoCfgDict, error) {
	var err error

	// read from the file only if the byte slice is empty
	// validate input file name
	if len(dict) == 0 {
		fileInfo, err := os.Stat(topoCfgDictFileName)
		if os.IsNotExist(err) || fileInfo.IsDir() {
			msg := fmt.Sprintf("topology dict %s does not exist or cannot be read", topoCfgDictFileName)
			fmt.Println(msg)

			return nil, fmt.Errorf(msg)
		}
		dict, err = os.ReadFile(topoCfgDictFileName)
		if err != nil {
			return nil, err
		}
	}
	example := TopoCfgDict{}

	// extension of input file name indicates whether we are deserializing json or yaml
	if useYAML {
		err = yaml.Unmarshal(dict, &example)
	} else {
		err = json.Unmarshal(dict, &example)
	}
	if err != nil {
		return nil, err
	}

	return &example, nil
}

// types used in linking to code that generates the starting structures rather than read from file
type BuildTopoCfgFuncType func(any) *TopoCfg
type BuildExpCfgFuncType func(any) *ExpCfg

// WriteToFile serializes the TopoCfg and writes to the file whose name is given as an input argument.
// Extension of the file name selects whether serialization is to json or to yaml format.
func (dict *TopoCfg) WriteToFile(filename string) error {
	// path extension of the output file determines whether we serialize to json or to yaml
	pathExt := path.Ext(filename)
	var bytes []byte
	var merr error

	if pathExt == ".yaml" || pathExt == ".YAML" || pathExt == ".yml" {
		bytes, merr = yaml.Marshal(*dict)
	} else if pathExt == ".json" || pathExt == ".JSON" {
		bytes, merr = json.MarshalIndent(*dict, "", "\t")
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

	return werr
}

// ReadTopoCfg deserializes a slice of bytes into a TopoCfg.  If the input arg of bytes
// is empty, the file whose name is given as an argument is read.  Error returned if
// any part of the process generates the error.
func ReadTopoCfg(topoFileName string, useYAML bool, dict []byte) (*TopoCfg, error) {
	var err error

	// read from the file only if the byte slice is empty
	// validate input file name
	if len(dict) == 0 {
		fileInfo, err := os.Stat(topoFileName)
		if os.IsNotExist(err) || fileInfo.IsDir() {
			msg := fmt.Sprintf("topology %s does not exist or cannot be read", topoFileName)
			fmt.Println(msg)

			return nil, fmt.Errorf(msg)
		}
		dict, err = os.ReadFile(topoFileName)
		if err != nil {
			return nil, err
		}
	}

	// dict has slice of bytes to process
	example := TopoCfg{}

	// input path extension identifies whether we deserialized encoded json or encoded yaml
	if useYAML {
		err = yaml.Unmarshal(dict, &example)
	} else {
		err = json.Unmarshal(dict, &example)
	}

	if err != nil {
		return nil, err
	}

	return &example, nil
}

// An ExpParameter struct describes an input to experiment configuration at run-time. It specified
//   - ParamObj identifies the kind of thing being configured : Switch, Router, Host, Interface, or Network
//   - Attribute identifies a class of objects of that type to which the configuration parameter should apply.
//     May be "*" for a wild-card, may be "name%%xxyy" where "xxyy" is the object's identifier, may be
//     a comma-separated list of other attributes, documented [here]
type ExpParameter struct {
	// Type of thing being configured
	ParamObj string `json:"paramObj" yaml:"paramObj"`

	// attribute identifier for this parameter
	Attribute string `json:"attribute" yaml:"attribute"`

	// ParameterType, e.g., "Bandwidth", "WiredLatency", "CPU"
	Param string `json:"param" yaml:"param"`

	// string-encoded value associated with type
	Value string `json:"value" yaml:"value"`
}

// CreateExpParameter is a constructor.  Completely fills in the struct with the [ExpParameter] attributes.
func CreateExpParameter(paramObj, attribute, param, value string) *ExpParameter {
	exptr := &ExpParameter{ParamObj: paramObj, Attribute: attribute, Param: param, Value: value}

	return exptr
}

// An ExpCfg structure holds all of the ExpParameters for a named experiment
type ExpCfg struct {
	// Name is an identifier for a group of [ExpParameters].  No particular interpretation of this string is
	// used, except as a referencing label when moving an ExpCfg into or out of a dictionary
	Name string `json:"expname" yaml:"expname"`

	// Parameters is a list of all the [ExpParameter] objects presented to the simulator for an experiment.
	Parameters []ExpParameter `json:"parameters" yaml:"parameters"`
}

func (excg *ExpCfg) AddExpParameter(exparam *ExpParameter) {
	excg.Parameters = append(excg.Parameters, *exparam)
}

// An ExpCfgDict is a dictionary that holds [ExpCfg] objects in a map indexed by their Name.
type ExpCfgDict struct {
	DictName string            `json:"dictname" yaml:"dictname"`
	Cfgs     map[string]ExpCfg `json:"cfgs" yaml:"cfgs"`
}

// CreateExpCfgDict is a constructor.  Saves a name for the dictionary, and initializes the slice of ExpCfg objects
func CreateExpCfgDict(name string) *ExpCfgDict {
	ecd := new(ExpCfgDict)
	ecd.DictName = name
	ecd.Cfgs = make(map[string]ExpCfg)

	return ecd
}

// AddExpCfg adds the offered ExpCfg to the dictionary, optionally returning
// an error if an ExpCfg with the same Name is already saved.
func (ecd *ExpCfgDict) AddExpCfg(ec *ExpCfg, overwrite bool) error {
	// allow for overwriting duplication?
	if !overwrite {
		_, present := ecd.Cfgs[ec.Name]
		if present {
			return fmt.Errorf("Attempt to overwrite template ExpCfg %s\n", ec.Name)
		}
	}
	// save it
	ecd.Cfgs[ec.Name] = *ec

	return nil
}

// RecoverExpCfg returns an ExpCfg from the dictionary, with name equal to the input parameter.
// It returns also a flag denoting whether the identified ExpCfg has an entry in the dictionary.
func (ecd *ExpCfgDict) RecoverExpCfg(name string) (*ExpCfg, bool) {
	ec, present := ecd.Cfgs[name]
	if present {
		return &ec, true
	}

	return nil, false
}

// WriteToFile stores the ExpCfgDict struct to the file whose name is given.
// Serialization to json or to yaml is selected based on the extension of this name.
func (ecd *ExpCfgDict) WriteToFile(filename string) error {
	pathExt := path.Ext(filename)
	var bytes []byte
	var merr error = nil

	if pathExt == ".yaml" || pathExt == ".YAML" || pathExt == ".yml" {
		bytes, merr = yaml.Marshal(*ecd)
	} else if pathExt == ".json" || pathExt == ".JSON" {
		bytes, merr = json.MarshalIndent(*ecd, "", "\t")
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
	return werr
}

// ReadExpCfgDict deserializes a byte slice holding a representation of an ExpCfgDict struct.
// If the input argument of dict (those bytes) is empty, the file whose name is given is read
// to acquire them.  A deserialized representation is returned, or an error if one is generated
// from a file read or the deserialization.
func ReadExpCfgDict(filename string, useYAML bool, dict []byte) (*ExpCfgDict, error) {
	var err error
	if len(dict) == 0 {
		dict, err = os.ReadFile(filename)
		if err != nil {
			return nil, err
		}
	}

	example := ExpCfgDict{}
	if useYAML {
		err = yaml.Unmarshal(dict, &example)
	} else {
		err = json.Unmarshal(dict, &example)
	}

	if err != nil {
		return nil, err
	}

	return &example, nil
}

// CreateExpCfg is a constructor. Saves the offered Name and initializes the slice of ExpParameters.
func CreateExpCfg(name string) *ExpCfg {
	expcfg := &ExpCfg{Name: name, Parameters: make([]ExpParameter, 0)}

	return expcfg
}

// ValidateParameter returns an error if the paramObj, attribute, and param values don't
// make sense taken together within an ExpParameter.
func ValidateParameter(paramObj, attribute, param string) error {
	// the paramObj string has to be recognized as one of the permitted ones (stored in list ExpParamObjs)
	if !slices.Contains(ExpParamObjs, paramObj) {
		return fmt.Errorf("paramater paramObj %s is not recognized\n", paramObj)
	}

	// Start the analysis of the attribute by splitting it by comma
	attrbList := strings.Split(attribute, ",")

	// every elemental attribute needs to be a name or "*", or recognized as a legitimate attribute
	// for the associated paramObj
	for _, attrb := range attrbList {

		// if name is present it is the only acceptable attribute in the comma-separated list
		if strings.Contains(attrb, "name%%") {
			if len(attrbList) != 1 {
				return fmt.Errorf("name paramater attribute %s paramObj %s is included with more attributes\n", attrb, paramObj)
			}

			// otherwise OK
			return nil
		}

		// if "*" is present it is the only acceptable attribute in the comma-separated list
		if strings.Contains(attrb, "*") {
			if len(attrbList) != 1 {
				return fmt.Errorf("name paramater attribute * paramObj %s is included with more attributes\n", paramObj)
			}

			// otherwise OK
			return nil
		}

		// otherwise check the legitmacy of the individual attribute.  Whole string is invalidate if one component is invalid.
		if !slices.Contains(ExpAttributes[paramObj], attrb) {
			return fmt.Errorf("paramater attribute %s is not recognized for paramObj %s\n", attrb, paramObj)
		}
	}

	// comma-separated attribute is OK, make sure the type of param is consistent with the paramObj
	if !slices.Contains(ExpParams[paramObj], param) {
		return fmt.Errorf("paramater %s is not recognized for paramObj %s\n", param, paramObj)
	}

	// it's all good
	return nil
}

// AddParameter accepts the four values in an ExpParameter, creates one, and adds to the ExpCfg's list.
// Returns an error if the parameters are not validated.
func (expcfg *ExpCfg) AddParameter(paramObj, attribute, param, value string) error {
	// validate the offered parameter values
	err := ValidateParameter(paramObj, attribute, param)
	if err != nil {
		return err
	}

	// create an ExpParameter with these values
	excp := CreateExpParameter(paramObj, attribute, param, value)

	// save it
	expcfg.Parameters = append(expcfg.Parameters, *excp)
	return nil
}

// WriteToFile stores the ExpCfg struct to the file whose name is given.
// Serialization to json or to yaml is selected based on the extension of this name.
func (dict *ExpCfg) WriteToFile(filename string) error {
	pathExt := path.Ext(filename)
	var bytes []byte
	var merr error = nil

	if pathExt == ".yaml" || pathExt == ".YAML" || pathExt == ".yml" {
		bytes, merr = yaml.Marshal(*dict)
	} else if pathExt == ".json" || pathExt == ".JSON" {
		bytes, merr = json.MarshalIndent(*dict, "", "\t")
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

	return werr
}

// ReadExpCfg deserializes a byte slice holding a representation of an ExpCfg struct.
// If the input argument of dict (those bytes) is empty, the file whose name is given is read
// to acquire them.  A deserialized representation is returned, or an error if one is generated
// from a file read or the deserialization.
func ReadExpCfg(filename string, useYAML bool, dict []byte) (*ExpCfg, error) {
	var err error
	if len(dict) == 0 {
		dict, err = os.ReadFile(filename)
		if err != nil {
			return nil, err
		}
	}

	example := ExpCfg{}
	if useYAML {
		err = yaml.Unmarshal(dict, &example)
	} else {
		err = json.Unmarshal(dict, &example)
	}

	if err != nil {
		return nil, err
	}

	return &example, nil
}

// ExpParamObjs, ExpAttributes, and ExpParams hold descriptions of the types of objects
// that are initialized by an exp file, for each the attributes of the object that can be tested for to determine
// whether the object is to receive the configuration parameter, and the parameter types defined for each object type
var ExpParamObjs []string
var ExpAttributes map[string][]string
var ExpParams map[string][]string

// GetExpParamDesc returns ExpParamObjs, ExpAttributes, and ExpParams after ensuring that they have been build
func GetExpParamDesc() ([]string, map[string][]string, map[string][]string) {
	if ExpParamObjs == nil {
		ExpParamObjs = []string{"Switch", "Router", "Host", "Interface", "Network"}
		ExpAttributes = make(map[string][]string)
		ExpAttributes["Switch"] = []string{"model", "*"}
		ExpAttributes["Router"] = []string{"model", "wired", "wireless", "*"}
		ExpAttributes["Host"] = []string{"*"}
		ExpAttributes["Interface"] = []string{"Switch", "Host", "Router", "wired", "wireless", "*"}
		ExpAttributes["Network"] = []string{"wired", "wireless", "LAN", "WAN", "T3", "T2", "T1", "*"}
		ExpParams = make(map[string][]string)
		ExpParams["Switch"] = []string{"execTime", "buffer", "trace"}
		ExpParams["Route"] = []string{"execTime", "buffer", "trace"}
		ExpParams["Host"] = []string{"CPU", "trace"}
		ExpParams["Network"] = []string{"media", "latency", "bandwidth", "capacity", "trace"}
		ExpParams["Interface"] = []string{"media", "latency", "bandwidth", "packetSize", "trace"}
	}

	return ExpParamObjs, ExpAttributes, ExpParams
}

// ReportErrs transforms a list of errors and transforms the non-nil ones into a single error
// with comma-separated report of all the constituent errors, and returns it.
func ReportErrs(errs []error) error {
	err_msg := make([]string, 0)
	for _, err := range errs {
		if err != nil {
			err_msg = append(err_msg, err.Error())
		}
	}
	if len(err_msg) == 0 {
		return nil
	}

	return errors.New(strings.Join(err_msg, ","))
}

// CheckDirectories probes the file system for the existence
// of every directory listed in the list of files.  Returns a boolean
// indicating whether all dirs are valid, and returns an aggregated error
// if any checks failed.
func CheckDirectories(dirs []string) (bool, error) {
	// make sure that every directory name included exists
	failures := []string{}

	// for every offered (non-empty) directory
	for _, dir := range dirs {
		if len(dir) == 0 {
			continue
		}

		// look for a extension, if any.   Having one means not a directory
		ext := filepath.Ext(dir)

		// ext being empty means this is a directory, otherwise a path
		if ext != "" {
			failures = append(failures, fmt.Sprintf("%s not a directory", dir))

			continue
		}

		if _, err := os.Stat(dir); err != nil {
			failures = append(failures, fmt.Sprintf("%s not reachable", dir))

			continue
		}
	}
	if len(failures) == 0 {
		return true, nil
	}

	err := errors.New(strings.Join(failures, ","))

	return false, err
}

// CheckReadableFileNames probles the file system to ensure that every
// one of the argument filenames exists and is readable
func CheckReadableFiles(names []string) (bool, error) {
	return CheckFiles(names, true)
}

// CheckOpututFileNames probles the file system to ensure that every
// argument filename can be written.
func CheckOutputFiles(names []string) (bool, error) {
	return CheckFiles(names, false)
}

// CheckFiles probes the file system for permitted access to all the
// argument filenames, optionally checking also for the existence
// of those files for the purposes of reading them.
func CheckFiles(names []string, checkExistence bool) (bool, error) {
	// make sure that the directory of each named file exists
	errs := make([]error, 0)

	for _, name := range names {

		// skip non-existent files
		if len(name) == 0 || name == "/tmp" {
			continue
		}

		// split off the directory portion of the path
		directory, _ := filepath.Split(name)
		if _, err := os.Stat(directory); err != nil {
			errs = append(errs, err)
		}
	}

	// if required, check for the reachability and existence of each file
	if checkExistence {
		for _, name := range names {
			if _, err := os.Stat(name); err != nil {
				errs = append(errs, err)
			}
		}

		if len(errs) == 0 {
			return true, nil
		}

		rtnerr := ReportErrs(errs)
		return false, rtnerr
	}

	return true, nil
}
