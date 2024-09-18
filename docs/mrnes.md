### Multi-Resolution Network Emulator/Simulator (mrnes)

#### Overview

The multi-resolution network emulator/simulator (mrnes) is a Golang package for modeling communication networks.  Its level of model abstraction is considerably higher than network simulators like ns3 or OpNet, for its intended use cases emphasize speed and scale of model evaluation more than quantitative predictive accuracy.  A **mrnes** model has components that represent various kinds of entities found in a complex system: routers, bridges, routers, repeaters, hub, switches, hosts, servers, sensors.  The configuration files read in at start-up identify the components being used,  their interconnections, and performance characteristics (like bandwidth) they are assigned to have.   A **mrnes** network simulates the transition of traffic between endpoints, and models the impact that contention for resources has on that traffic.   The mrnes package does not contain the definition of components that generate and receive network traffic, these must be supplied by code that imports the mrnes package (e.g., various components of the pies package).

An important difference between **mrnes** and other network simulators is that it does not assume IP addressing.  Endpoints that send and receive traffic have unique string-based names (which *might* be IP addresses, but need not be) and those global names serve as the addressing points.  One can choose to build into mrnes models refinements of this, e.g. including additional labels that are used like MAC addresses, or other labels that play roles like TCP port numbers.   **mrnes** leaves to the modeler decisions of what to do when traffic is lost due to contention,  providing that user with a means of receiving notice of the network location and simulation time when such loss occurs.

**mrnes** network traffic has two representations.  One is of a packet, modeled after an Ethernet frame.  Treatment of a packet is handled much like any other discrete-event network simulator. Movement of a packet through the network is accomplished with a discrete event simulation where the events describe a packet leaving the device which generated it, entering an interface, leaving an interface, entering a network abstraction, entering the device named as the packet's destination.  Delays may be introduced between successive events to capture time elapses due to queuing, processing (e.g. being moved between interfaces in a switch or router), and passage through interfaces as a function of the message length and interface bandwidth.

The "Multi-Resolution" component of **mrnes** is due to its inclusion of traffic flows.   A flow has a device as its source and another as its destination, and at any instant in the simulation is described by an average bitrate.   Flows follow the same routes as packets, passing through the same interfaces, devices, and networks.   Discrete events set flows up, and other events change their rates and tear them down.   Flows typically have longer time-scales than packets  Flows affect each other and packets through their consumption of interface and network bandwidth.   When a flow is established its existence and bitrate is noted at every interface, device, and network on its route.   When simulating the passage of a packet through an interface,  **mrnes** accounts for the competition it faces for the interface's bandwidth service from other packets and flows.  Likewise a mrnes modeler can communicate the rate a stream of individual packets are pushed through the network, and the average bit rate of that stream is accounted for in a flow's own competition for bandwidth.

#### mrnes API

An **mrnes** modeler interacts with the package through methods defined by an mrnes struct named `NetworkPortal`, using structs also defined by `mrnes`. The modeler's code creates such a struct through a call `mrnes.CreateNetworkPortal()`.  The methods, types, and structs below are all given in Go, as a modeler who is building an application that uses the `mrnes` package will be defining and using these entities from an application written in Go.

Methods of particular interest to the modeler are

- `func (np *NetworkPortal) EndptCPUModel(devName string) string` .  A **pces** modeler sees the mapping file which binds **pces** functions to Endpts found in the network topology.  Specifics of the CPU on that Endpt are needed for the **pces** model to look up its execution time on that CPU.  This method provides that description of the CPU.
- ` func LimitingBndwdth(srcDevName, dstDevName string) float64` .  When creating a flow, a `pces` modeler requests a certain amount of bandwidth for that flow, at each point along its route.  This function can be called to inform the modeler of the maximum flow it can be allowed (which is the minimum maximum bandwidth at each interface and network along the route).
- `func (np *NetworkPortal) EnterNetwork(evtMgr *evtm.EventManager, srcDev, dstDev string, msgLen int, connDesc *ConnDesc, IDs NetMsgIDs, rtns RtnDescs, requestRate float64, msg any) int` .  This method is called to request the network simulator to transport a message.  Meaning of the input parameters
  - `evtMgr`  points to the data structure organizing all the discrete events, both in `mrnes` and in the modeler's program that calls `EnterNetwork`.
  - `srcDev`, `dstDev` are the names of the Endpts in the `mrnes` topology serving as the source and destination of the message.
  - `msgLen` gives the length of the message to be carried, in bytes.
  - `connDesc` points to a structure that holds description of the message as defined by the `mrnes` structure `ConnDesc`.  The structure has enumerated constants (defined by `mrnes` ) that describe the type of the connection (e.g. a discrete packet or some kind of flow), identification of how latency will be approximated (e.g. direct simulation, or a particular kind of quantitative approximation), and (in the case of a flow) whether the flow is being set up, torn down, or having its requested bitrate changed).
  - `IDs` is a structure carrying particular identifiers for the requested connection that are used by the simulator.
  - `rtns` is a structure holding pointers to `mrnes` structures of type `RtnDesc`, each of which holds parameters needed to schedule an event.  Each structure holds the parameters for different driving circumstances, include when the message is finally delivered, and optionally, in the case of a flow, to report to the specified event handlers if the flow's bitrate is forced to change, and optionally, if the packet is dropped during transmission.
  - `requestRate` is a bitrate the source requests for a flow.  Note, it may not receive the entire rate, depending on the state of the network at the time of the request. 
  - `msg` carries a structure that the modeler wishes to have conveyed by the network from the srcDev to the dstDev. `mrnes` makes no assumptions about this struct, it just delivers it.


To formulate a call to EnterNetwork a modeler needs access to certain `mrnes` defined enumerated constants and structures.

##### `RtnMsgStruct`

```
// RtnMsgStruct formats the report passed from the network to the
// application calling it
type RtnMsgStruct struct {
    Latency float64     // span of time (secs) from srcDev to dstDev
    Rate    float64     // minimum available bandwidth encountered during transit
    PrLoss  float64     // est. prob. of drop in transit
    Msg     any         // msg introduced at EnterNetwork
}
```

At the point the simulator realizes that the message it is carrying has reached the destination and should passed back to the calling application, it creates an instance of  `RtnMsgStruct` to hold a description of the message delivery. `Msg` carries the struct passed to the simulator in the `EnterNetwork` call.  `Latency` gives the measured (or approximated) length of time (in seconds) between the message being introduced at the source and being delivered at the destination.  `Rate` gives the observed minimum bandwidth allocated to the message anywhere along its route. `PrLoss` gives an approximation of the probability of the packet being dropped anywhere along the route, based on formulaes from queueing theory.  

`RtnDesc`

```
// RtnDesc holds the context and event handler for scheduling a return
type RtnDesc struct {
  Cxt any
  EvtHdlr evtm.EventHandlerFunction
```

A call to the `evtm.EventManager` event scheduler requires specification of some (general) context (often a pointer to some structure that describes the entity to which the event will be applied), and a function that satisfies the required signature.  A modeler creates instances of `RtnDesc` structures to populate an instance of the `RtnDescs` structure.

`RtnDescs`

```
// RtnDescs hold four RtnDesc structures, for four different use scenarios.
type RtnDescs struct {
    Rtn *RtnDesc
    Src *RtnDesc
    Dst *RtnDesc
    Loss *RtnDesc
}   
```

The `Rtn` pointer is used to schedule report of the completion of the message delivery to the destination, along with an instance of `RtnMsgStruct`. `Src` and `Dst` labeling suggested they be used to notify a message's source and destination in the case of a triggering event (like changes in flow bitrates). Likewise the `Loss` structure is used to report when there is a packet loss.

`ConnType`

```
type ConnType int
const (
    MajorFlowConn ConnType = iota
    MinorFlowConn
    DiscreteConn
)
```

Traffic is tagged as being discrete or flow, and with 'flow' either 'major' or 'minor'.  This latter difference defines how the flow competes with others for bandwidth.

`ConnLatency`

```
type ConnLatency int
const (
    Zero ConnLatency = iota
    Place
    Simulate
)
```

The latency of a message transfer can be modeled one of three ways. `Zero` means it is instaneous.  This makes sense for the establishment and changes to flow whose time-scales for existence or rate chaning are large compared to the 'real' time scale of network communication.   `Place`  means that the message is placed directly at the destination without performing discrete events at network devices, using a non-zero latency approximation based on analysis of the network state.  Choice of `Place` latency trades off detail in model exploration with computational speed, in part because it ignores changes in the network state that might occur between sending and receiving.  `Simulate` means the message traversal will be simulated in the usual way of discrete-event simulation, passing from device to device along its route.

`FlowAction`

```
// FlowAction describes the reason for the flow message, that it is starting, ending, or changing the request rate
type FlowAction int             
const (
    None FlowAction = iota
    Srt        
    Chg 
    End
)   
```

`FlowAction` has meaning for flow connections, indicating whether they start, end, or are changing the bitrate.  The `None` value is used when the connection is not a flow.

 `ConnDesc`

```
type ConnDesc struct { 
    Type ConnType
    Latency ConnLatency
    Action FlowAction
}   
```

ConnDesc holds characteristics of a connection...the type (discrete or flow), the latency (how delay in delivery is ascribed) and in the case of a flow, the action (start, end, rate change).

`NetMsgIDs`

```
type NetMsgIDs struct {
    ExecID int      // execution id, from application
    MajorID int     // major flow id
    MinorID int     // minor flow id
    ConnectID int   // connection id
}
```

A call to `EnterNetwork` carries a collection of identifiers.   `ExecID` is used by `pces` to associate the passage of a `pces.CmpPtnMsg` between Funcs.  That identifier is needed when the message emerges from the network simulator, but is otherwise ignored by it. `MajorID` and `MinorID` refer to flows created by the application modeler.  More detailed on flows and the meaning of these identifiers is given later. `ConnectID` is a unique identifier given to every request to establish a new association between some srcDev and dstDev.  It is possible for a connection established by an `EnterNetwork` call to be used more than once, and so after `mrnes` creates the connectionID and returns to the `EnterNetwork` caller,  a later call can refer to that connection by including its ID in the `ConnectID` field.

#### mrnes input files

File topo.yaml holds information describing the computers and networks in the pces/mrnes model.  The highest level structure is a dictionary TopoCfg with a name attribute, and then lists of dictionaries for each device type.  Another file, exp.yaml, holds descriptions of parameter value assignments to be made to topology components, e.g., interface bandwidth.   We discuss the topology description file first.

The descriptions below are of dictionaries defined in TopoCfg, using YAML-like notation. `mnres` accepts json descriptions as well, so the definitions are to the largest extent identifying the key-value associations one finds in both YAML and JSON.  Note also that brackets [ ] denote a list, and what we provide below are labels we define in the section on [Identity Sets](#Identity Sets).

**TopoCfg**

```
name: string
networks: [NetworkDesc]
routers: [RouterDesc]
endpts: [EndptDesc]
switches: [SwitchDesc]
```

**NetworkDesc**

A mrnes network description lists the string-valued identifiers of all the devices (endpoints, routers, switches) it encompasses.  It has an key `netscale` whose value inexactly describes its size (e.g., "LAN", "WAN", "T3"), and a key`mediatype` whose value describes the physical media (e.g., "wired", "wireless").  The methodologies for initializing parameters of networks and devices may refer to modeler-defined "groups", e.g., indicating that the base basewidths for all networks belonging to group "UIUC" is 1000 Mbps.  A network may belong to any number of groups the model includes.

```
name: NETWORKNAME
groups: [GROUPNAME]
netscale: NETSCALE
mediatype: MEDIA
endpts: [ENDPTNAME]
routers: [ROUTERNAME]
switches: [SWITCHNAME]
```

**EndptDesc**

In mrnes we use a general description of an "endpoint" to refer to any device that is performing computation.   It might be a low-powered host or even lower-powered sensor, or may be an enterprise server.   From the mrnes point of view the important thing is that it is a platform for executing computations and that it connects to at least one network.   The processing simulated at an endpoint depends on the number of cores that are assumed to be in use (as the model supports parallel execution of computational tasks) and so the description names the number of cores.   The endpoint has at least one network interface, possibly more, and so the EndptDesc includes a list of interface descriptions. Finally, the model of the CPU used by the Endpt is given, as this is needed when looking up the execution times of computations performed on that Endpt.

```
name: ENDPTNAME
groups: [GROUPNAME]
cpumodel: CPUMODEL
cores: int
interfaces: [IntrfcDesc]
```

**RouterDesc**

Like a NetworkDesc, a RouterDesc has a unique name identifier, a list of groups the modeler declares the router to belong to,  and a descriptor of the router's model.   It also includes a list of descriptions of the network interfaces attached to the device.

```
name: ROUTERNAME
groups: [GROUPNAME]
model: ROUTERMODEL
interfaces: [IntrfcDesc]
```

**SwitchDesc**

The attributes of a SwitchDesc are identical to those of a RouterDesc.

```
name: SWITCHNAME
groups: [GROUPNAME]
model: SWITCHMODEL
interfaces: [IntrfcDesc]
```

**IntrfcDesc**

The interface description dictionary includes keys whose values identify the device type and instance to which it is attached, the name and type of media of the network to which it is attached.  Like other components of a network it carries a list of group names the model may refer to when configuring parameters for the interface in the run-time representation.

```
name: string
groups: [GROUPNAME]
devtype: DEVTYPE
device: DEVNAME
cable: INTRFCNAME
carry: INTRFCNAME
wireless: [INTRFCNAME]
faces: NETNAME
```

Exactly one of the values of keys `cable`, `carry`, and `wireless` attributes is non-empty.   In the case of the interface being connected to a wireless network the `wireless` list includes the names of all the interfaces also on that network that a transmission from the interface may directly reach.  If instead the interface connects to a wired network, the mrnes view is that the interface may be directly connected through a cable to some other interface, and if that is the case the `carry` attribute names that interface.   mrnes allows also the declaration that the media is wired, but that the level of detail in the model does not include declaration of cables and the interfaces they connect, but does allow one to name another interface on that same network to which transmissions are forwarded.  This interface is named by the value of the  `carry` key.

Finally, the name of the network the interface touches is expressed in the value of the `faces` key.

####  exp.yaml

When a `mrnes` model is loaded to run, the file exp.yaml is read to find performance parameters to assign to network devices, e.g., the speed of a network interface. mrnes uses a methodology to try and be efficient in the specification, rather than directly specify every parameter for every device.  For example, we can declare wildcards, e.g., with one statement that every wireless interface in the model has a bandwidth of 100 Mbps.

The methodology is for each device type to read a list of parameter assignments.  A given assignment specifies a constraint, a parameter, and a value to assign to that parameter.   Only devices that satisfy the constraint are eligible to have that parameter assigned.   Possible parameters vary with the device type.  A table of assignable parameters as function of device type is given below.

| Device Type | Possible Parameters            |
| ----------- | ------------------------------ |
| Network     | latency, bandwidth,  trace     |
| Interface   | latency, bandwidth, MTU, trace |
| Endpt       | trace, model                   |
| Switch      | buffer, trace                  |
| Router      | buffer, trace                  |

For a Network, the 'latency' parameter is the default latency for a message when transitioning between interfaces that face the given network.  A value assigned is in units of seconds. 'bandwidth' quantifies modeled point to point bandwidth across the network between two specfied interfaces. 'trace' is a flag set when detailed trace information is desired for traffic crossing the network.

For an Interface the 'latency' parameter is the time for the leading bit of a message to traverse the interface (in units of seconds), 'bandwidth' is the speed of the interface (in units of Mbps), MTU is the usual maximum transmission unit enforced by interfaces.

For an Endpoint the 'model' parameter refers to the endpoint  `model`  attribute in EndptDesc.

For Switch and Router the 'buffer' parameter places an upper bound on the total number of bytes of traffic that one of these devices can buffer in the presence of congestion.

The constraint associcated with an assignment is that a device match specifications in each of the named attributes of the device.  The attributes that can be tested are listed below, as a function of the device type.

| Device Type | Testable Attributes           |
| ----------- | ----------------------------- |
| Network     | name, group, media, scale, *  |
| Interface   | name, group, device, media, * |
| Endpt       | name, group, model, *         |
| Switch      | name, group, model, *         |
| Router      | name, group, model, *         |

To limit assignment of a parameter to one specific device, the constraint would indicate that a device's 'name' be compared to the value given in the constraint.  At most one device will have the specified name and have the parameter assigned.   At the other end of the spectrum selecting wildcard * as the testable attribute means the match is  satisfied.   One can make an assignment to all devices with the same model type by selecting 'model' as the attribute and the model identify of interest as the matching value.  Every one of the network devices can be selected based on presence of a particular group name in its `groups` list (selecting group as the testable attribute and the group name of interest as the value).   Furthermore, the constraint may specify a number of testable attribute / value pairs, with the understanding the the constraint is met only if each individual matching test passes.

The modeler may place the parameter assignment statements in any order in the file.  However, before application the lists are ordered, in a 'most general first' ordering.   Specifically, for any given constrained assignment there are a set of devices that satisfy its constraints.   We order the assignments in such a way that for a given parameter P and device type, if the constraints of assignment A1 match a super-set of those which match assignment A2, then A1 appears before A2 in the ordering.  What this means is that if we then step linearly through the post-order listing of assignments and apply each, then a more refined assignment (i.e. one that applies to fewer devices) is applied later, overwriting any earlier assignment.  In this way we can efficiently describe large swathes of assignments, applying parameters by wildcard or broad-brush first, and then refine that assignment more locally where called for.

The dictionaries that describe these assignments are found in file exp.yaml, in an encompassing dictionary ExpCfgDict.

**ExpCfgDict**

The exp.yaml file contains a dictionary comprised of a name, and a dictionary of experimental parameter dictionaries, indexed by the name associated with the parameter.

```
dictname: string
cfgs: 
	EXPCFGNAME: ExpCfg
```

**ExpCfg**

The ExpCfg dictionary has a name, and a list of dictionaries that describe parameters to be applied

```
name: string
parameters: [ExpParameter]
```

**ExpParameter**

An ExpParameter dictionary identifies the type of network object the parameter applies to, a list of attribute constraints that need to be satisifed for an object in that class to receive the parameter, the identity of the parameter to be set, and the value to be set to that parameter on all network objects of the named type that satisfy the attribute constraint.

```
paramobj: NETWORKOBJ
attributes: [AttrbStruct]
param: ATTRIB
value: string
```

Note again that one AttrbStruct dictionary specifies one constraint, the list of AttrbStruct dictionaries associated with dictionary key 'attributes' means that all those constraints must match for an object's `param` to be set by `value`.

**AttrbStruct**

A single AttrbStruct record identifies an attribute and a value that attribute must have for a match.

```
attrbname: ATTRIB
attrbvalue: string
```



### Flows

`mrnes` includes the notion of flows to enable efficient representation of traffic at a coarser granularity than packets.  One motivation for flows is to be able to represent the effect that background network traffic has on more detailed traffic that is represented by packets.   This allows a modeler to assess the impact that network context conditions have on traffic that is presented with more detail, by holding the presentation of the detailed traffic constant, as different experiments vary the intensity and placement of background flows.

 `mrnes` defines three types of flow, a *reservation*, a *major* flow and a *minor* flow.  All types of flow have a source device, destination device, and route just as does a packet, and both have a bitrate.   The flow is modeled to be passing through every interface, device, and network on its route sustaining that bitrate.   When the executing model establishes a new flow (of any kind) it requests a bit-rate.   Provisioning of the flow considers the presence of other flows at network points that intersect the new flow, and their own established bitrates may limit the *accepted rate* that can be provisioned for the new flow.  The accepted rate is the same for the flow at every point on its route.  This rule models the effect that otherwise unmodeled mechanisms like TCP have on establishing a sustainable inject rate at the flow's source.

The `mrnes` API allows a modeler to specify that a packet introduced to the network is associated with a flow.  The result is that the packet is treated as being part of a stream of packets where the average bitrate of the stream is the flow's bitrate.   For the purposes of estimating latency and packet loss probability

The difference between flow types is in the logic used to allocate and sustain their bitrates.

hree things are important for both kinds of flows. First, that that accepted bitrate a flow is allocated is the same throughout its entire route.   This rule models the effect that otherwise unmodeled mechanisms like TCP have on establishing a sustainable inject rate at the flow's source.  Second, that the accepted bitrate may be less than the requested bitrate, owing to contention. Third, that a flow's accepted rate may be interdependent with other flows' accepted rates, according to rules that allocate bandwidth under contention.  This means that a computation that establishes or changes the accepted bitrate for one flow may cause changes in the bitrates of certain other flows.

##### Reservation

A reservation flow, once established, retains it accepted rate until the model explicitly removes the reservation or is successful in changing the reservation's bitrate.   As we will see, unlike the other types of flows a reservation is impervious to changes in the bitrates of other flows.   Reservation flows are a simple and efficient way of modeling that background traffic consumes resources and impacts the performance of packet-oriented traffic.   Reservations can be used to highlight the presence of traffic classes that receive priority.

A reservation is requested through the `mrnes` API and names a bitrate it wishes to sustain.  The `mrnes` logic determines whether the request bandwidth is available at every point on the flow's route.  If so, the requested bitrate is reserved at every point on the route and reports the success through the API. Bandwidth allocated for a reservation is unavailable to any other flow.   From the point of view of packets not associated with the reservation or other flows it is as though the interfaces and networks simply have less bandwidth to offer.

The `mrnes` API allows a modeler to specify that a packet introduced to the network is associated with a flow

##### Major Flows

Major flows and minor flows differ in the logic used to establish their accepted rates.   When there are N major flows whose routes all pass through an interface in the same direction (i.e., ingress or egress), each arrives with an accepted bitrate, and the sum of those bitrates cannot exceed the interface bandwidth.   In a network where traffic all has the same priority, we expect that bandwidth will be received in proportion to the asking rates.  That means that if major flow F1 has a requested bitrate of R1 and accepted bitrate of A1, and major flow F2 has a requested bitrate of R2 and accepted bitrate of A2, and if F1 and F2 pass through the same interface in the same direction (i.e., both ingress or both egress) then A1/A2 = R1/R2.   In the case of contention at the interface this means that the fraction of interface bandwidth allocated to a flow is the ratio of the flow's requested rate to the sum of the requested rates of all other flows passing through that interface, in that direction.

 At the time of a new major flow F1's creation, deletion, or change in requested rate, a computation is performed that defines its accepted end-to-end bitrate.  The accepted bitrate will be the largest possible which is no greater than F1's requested rate, and also satisfies the bandwidth allocation constraint above at all of the interfaces (and networks) on the flow's route.   This means that if major flow F2 passes through the same interface in the same direction as F1 at an interface (or network), then if there was congested there either before or after F1's change in request rate, F2's accepted rate will have to be recomputed also.

Major flows are useful for modeling the impact of up long-lived bulk transfers, or long-lived periodic interactions between a client and server.  They average out the bandwidth requirements of the underlying packets, but provide computational savings at the cost of not capturing the potential for burstiness in those packets, and the effect that might have on attributes such as packet loss probability.  It is a modeler's job to choose the level of detail (and computation) to provide these connections.

Major flows are useful for establishing the presence of background traffic that consumes network resources and impact more detailed traffic.   For a given scenario with detailed traffic connections one can create experiments that differ in the overall system load, by increasing or decreasing the major flow request rates.

Another useful function for a major flow is to use it to model the impact of bandwidth reservation.  A certain class of connection might reserved preferred traffic.   A version of a major flow called a "Reservation" differs from other major flows in that once established with a bitrate R, no other changes in the composition or bitrates of other flows can change that allocation of R.



Major Flows



### Identity Sets

Below we describe each identity set and define the set of strings it represents.

| Identity Set | Composition                                                  |
| ------------ | ------------------------------------------------------------ |
| NETWORKNAME  | All values mapped to by 'name' key in NetworkDesc dictionaries (3.2) |
| ENDPTNAME    | All values mapped to by 'name' key in EndptDesc dictionaries (3.3) |
| CPUMODEL     | All values mapped to by 'model' key in EndptDesc dictionaries (3.3) |
| ROUTERNAME   | All values mapped to by 'name' key in RouterDesc dictionaries (3.4) |
| ROUTERMODEL  | All values mapped to by 'model' key in RouterDesc dictionaries (3.4) |
| SWITCHNAME   | All values mapped to by 'name' key in SwitchDesc dictionaries (3.5) |
| SWITCHMODEL  | All values mapped to by 'model' key in SwitchDesc dictionaries (3.5) |
| INTRFCNAME   | All values mapped to by 'name' key in IntrfcDesc dictionaries (3.6) |
| MEDIA        | {"wired", "wireless"}                                        |
| NETSCALE     | {"LAN", "WAN", "T3", "T2", "T1"}                             |
| DEVTYPE      | {"Endpt", "Router", "Switch"}                                |
| DEVNAME      | Union of sets ENDPTNAME,  ROUTERNAME,  SWITCHNAME            |
| GROUPNAME    | All values in lists mapped to by key 'groups' in NetworkDesc, EndptDesc, RouterDesc, SwitchDesc,  and IntrfcDest dictionaries (3.2, 3.3, 3.4, 3.5, 3.6) |
| NETWORKOBJ   | {"Network", "Endpt", "Router", "Switch", "Interface"         |
| ATTRIB       | {"name", "group", "device", "scale", "model", "media", "*"}  |
| OPCODE       | {"route", "switch"}                                          |
| DEVMODEL     | Union of SWITCHMODEL and ROUTERMODEL                         |





 
