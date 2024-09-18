package mrnes

// transition.go holds state and code related to the transition of
// traffic between the application layer and the mrnes layer,
// and contains the methods involved in managing the 'flow' representation of traffic
//
import (
	"fmt"
	"github.com/iti/evt/evtm"
	"github.com/iti/evt/vrtime"
	"golang.org/x/exp/slices"
	"math"
)

// Traffic is tagged as discrete or flow
type ConnType int
const (
	_ = iota
	FlowConn ConnType = iota
	Reservation
	DiscreteConn
)

// ConnLatency describes one of three ways that latency is ascribed to
// a source-to-destination connection.  'Zero' ascribes none at all, is instantaneous,
// which is used in defining major flow's to reserve bandwidth.   'Place' means
// that at the time a message arrives to the network, a latency to its destination is
// looked up or computed without simulating packet transit across the network.
// 'Simulate' means the packet is simulated traversing the route, through every interface.
type ConnLatency int
const (
	_ = iota
	Zero ConnLatency = iota
	Place	
	Simulate	
)

// a rtnRecord saves the event handling function to call when the network simulation
// pushes a message back into the application layer.  Characteristics gathered through
// the network traversal are included, and so available to the application layer
type rtnRecord struct {
	pckts int
	prArrvl float64
	rtnFunc evtm.EventHandlerFunction
	rtnCxt  any
	rtnID   int
	rtnLoss *float64
	rtnData any
}

// RprtRate is the structure of a message that is scheduled for delivery
// as part of a 'Report' made when a flow rate changes or a packet is lost
type RprtRate struct {
	FlowID int
	MbrID  int
	AcceptedRate float64
	Action FlowAction	
}


// ConnDesc holds characteristics of a connection...the type (discrete or flow),
// the latency (how delay in delivery is ascribed) and in the case of a flow,
// the action (start, end, rate change)
type ConnDesc struct {
	Type ConnType
	Latency ConnLatency
	Action FlowAction
}

// RtnDesc holds the context and event handler 
// for scheduling a return
type RtnDesc struct {
	Cxt any
	EvtHdlr evtm.EventHandlerFunction
}

// RtnDescs hold four RtnDesc structures, for four different use scenarios.
// Bundling in a struct makes code that uses them all more readable at the function call interface
type RtnDescs struct {
	Rtn *RtnDesc
	Src *RtnDesc
	Dst *RtnDesc
	Loss *RtnDesc
}	

// NetMsgIDs holds four identifies that may be associated with a flow.
// ExecID comes from the application layer and may tie together numbers of communications
// that occur moving application layer messages between endpoints. FlowID 
// refer to a flow identity, although the specific value given is created at the application layer
// (as are the flow themselves).   ConnectID is created at the mrnes layer, describes a single source-to-destination
// message transfer
type NetMsgIDs struct {
	ExecID int		// execution id, from application
	FlowID int		// flow id
	ConnectID int	// connection id
}

func RequestRsrvn() {
	// if reservation is true, the flow is a flow, and the flow action is Srt
	// check to see whether reservation can be satisfied after the existing allocation
	// to the reserved flow is removed
	if classID && connDesc.Action != End {
		available := LimitingBndwdth(srcDev, dstDev, np.AcceptedRate[flowID]) 
		if available < requestRate {
			return -1, 0.0, false
		}
	}
}

// EnterNetwork is called after the execution from the application layer
// It creates NetworkMsg structs to represent the start and end of the message, and
// schedules their arrival to the egress interface of the message source endpt

// Two kinds of traffic may enter the network, Flow and Discrete
// Entries for a flow may establish a new one, modify an existing one, or delete existing ones.
// Messages that notify destinations of these actions may be delivered instantly, may be delivered using
// an estimate of the cross-network latency which depends on queueing network approximations, or may be
// pushed through the network as individual packets, simulated at each network device.
//
// input connType is one of {Flow, Discrete}
//       flowAction is one of {Srt, End, Chg}
//       connLatency is one of {Zero, Place, Simulate}
//
// We approximate the time required for a packet to pass through an interface or network is a transition constant plus
// the mean time in an M/D/1 queuing system.  The arrivals are packets whose length is the frame size,
// the deterministic service time D is time required to serve a packet with a server whose bit service rate
// is the bandwidth available at the interface or network to serve (meaning the total capacity minus the
// bandwidth allocations to other flows at that interface or network), and the arrival rate is the accepted
// rate allocated to the flow.
// 
// Description of possible input parameters
//
// | Message         | connType      | flowAction    | connLatency           | flowID
// | --------------- | ------------- | ------------- | --------------------- | ----------------------- |
// | Discrete Packet | DiscreteConn  | N/A           | Zero, Place, Simulate | >0 => embedded          |
// | Major Flow      | FlowConn | Srt, Chg, End | Zero, Place, Simulate | flowID>0                |
//
func (np *NetworkPortal) EnterNetwork(evtMgr *evtm.EventManager, srcDev, dstDev string, msgLen int, 
	connDesc *ConnDesc, IDs NetMsgIDs, rtns RtnDescs, requestRate float64, classID int, msg any) (int, float64, bool) {
	
	// if connectID>0 make sure that an entry in np.Connections exists
	_, present := np.Connections[connectID]
	if connectID>0 && !present {
		panic(fmt.Errorf("non-zero connectID offered to EnterNetwork w/o corresponding Connections entry"))
	}

	// pull out the IDs for clarity
	flowID := IDs.FlowID
	connectID := IDs.ConnectID

	// if flowID >0 and flowAction != Srt, make sure that various np data structures that use it for indexing exist
	if flowID > 0 && connDesc.Action != Srt {
		_, present0 := np.RequestRate[flowID]
		_, present1 := np.AcceptedRate[flowID]
		_, present2 := np.FlowCoeff[flowID]
		if !(present0 && present1 && present2) { 
			panic(fmt.Errorf("flowID>0 presented to EnterNetwork without supporting data structures"))
		}
	}

	// is the message about a discrete packet, or a flow?
	isPckt := (connDesc.Type == DiscreteConn)

	// find the route, which needs the endpoint IDs
	srcID := TopoDevByName[srcDev].DevID()
	dstID := TopoDevByName[dstDev].DevID()
	route := findRoute(srcID, dstID)

	// make sure we have a route to use
	if route == nil || len(*route) == 0 {
		panic(fmt.Errorf("unable to find a route %s -> %s", srcDev, dstDev))
	}

	// take the frame size to be the minimum of the message length
	// and the minimum MTU on interfaces between source and destination
	frameSize := FindFrameSize(flowID, route)
	if isPckt && msgLen < frameSize {
		frameSize = msgLen
	} 
		
	// number of frames for a discrete connection may depend on the message length,
	// all other connections have just one frame reporting the change
	numFrames := 1
	if connDesc.Type == DiscreteConn {
		numFrames = msgLen/frameSize
		if msgLen%frameSize > 0 {
			numFrames += 1
		}
	}

	np.RequestedRate[flowID] = requestRate
	// in the case this is a reservation and a starting action,
	// passing the test above implies that the requested rate
	// is acceptable
	// see if a connectionCode needs to be generated
	if !(connectID > 0) {

		// tell the portal about the arrival, passing to it a description of the
		// response to be made, and the number of frames of the same message that
		// need to be received before reporting completion
		connectID = np.Arrive(rtns, numFrames)

		// remember the flowIDs, given the connectionID
		np.Connections[connectID] = flowID
		np.InvConnection[flowID] = connectID
	}	

	// Flows and packets are handled differently
	if connDesc.Type == FlowConn {
		np.FlowEntry(evtMgr, srcDev, dstDev, msgLen, connDesc, 
			flowID, classID, connectID, requestRate, route, msg)
		return connectID, np.AcceptedRate[flowID], true
	}

	// get the interface through which the message passes to get to the network.
	// remember that a route step names the srcIntrfcID as the interface used to get into a network,
	// and the dstIntrfcID as the interface used to ingress the next device
	intrfc := IntrfcByID[(*route)[0].srcIntrfcID]

	// ordinary packet entry; make a message wrapper and push the message at the entry
	// of the endpt's egress interface.  Segment the message into frames and push them individually
	delay := float64(0.0)

	// if the packet is bound to a major flow and has a request rate, ensure that the request
	// rate is not larger than the available bandwidth of the flow.
	if requestRate > 0 && flowID > 0 {
		mfp := np.FlowPortal[flowID]
		var used float64
		for _, rate := range mfp.AcceptedRate {
			used += rate
		}
		available := np.AcceptedRate[flowID]-used
		if !(available > 0 ) {
			return -1, 0.0, false
		}
		requestRate = math.Min(requestRate, np.AcceptedRate[flowID]-used)
	}

	if requestRate > 0 && !(flowID > 0) {
		availbndwdth := AvailBndwdth(srcDev, dstDev)
		if !(availbndwdth > 0 ) {
			return -1, 0.0, false
		}
		requestRate = math.Min(requestRate, availbndwdth)
	}

	for fmNumber:=0; fmNumber<numFrames; fmNumber++ {
		nm := new(NetworkMsg)
		nm.StepIdx = 0
		nm.Route = route
		nm.SrvRate  = -1.0
		nm.PcktRate = requestRate
		nm.PrArrvl = 1.0
		nm.StartTime = evtMgr.CurrentSeconds()
		nm.MsgLen = frameSize
		nm.ConnectID = connectID
		nm.FlowID = flowID
		nm.Connection = *connDesc
		nm.PcktIdx = fmNumber
		nm.NumPckts = numFrames
		nm.Msg = msg

		// schedule the message's next destination 
		np.SendNetMsg(evtMgr, nm, delay)

		// how long to get through the device to the interface?
		// ServiceRate gives available bandwidth as a function of the connection type and embedding
		// Notice accumulation of delay for each successive frame, meaning that one frame needs to get completely
		// out before the next one begins
		delay += (float64(frameSize*8) / 1e6) / intrfc.ServiceRate(nm, false)
		delay += intrfc.State.Delay
	}	
	return connectID, requestRate, true
}


// FlowEntry handles the entry of major flows to the network
func (np *NetworkPortal) FlowEntry(evtMgr *evtm.EventManager, srcDev, dstDev string, msgLen int,
	connDesc *ConnDesc, flowID int, classID int, connectID int, 
		requestRate float64, route *[]intrfcsToDev, msg any) { 

	// set the network message and flow connection types
	flowAction := connDesc.Action

	reservation := (classID > 0)

	// revise the requested rate for the major flow
	np.RequestRate[flowID] = requestRate

	// Setting up the Major Flow on Srt
	if flowAction == Srt {
		// include a new flow into the network infrastructure.
		// return a structure whose entries are used to estimate latency when requested
		np.FlowCoeff[flowID] = BuildFlow(flowID, classID, route)
	}

	// change the flow rate for the flowID and take note of all
	// the major flows that were recomputed
	chgFlowIDs := np.EstablishFlowRate(evtMgr, flowID, classID, requestRate, reservation, route, flowAction)

	// For each of the modified flows recompute the utilization vector
	// for use in the 'Place' latency model. 
	for flwID := range chgFlowIDs {
		rhoVec := np.ComputeRhoVec(flwID, np.AcceptedRate[flwID], route)

		// save that vector in the portal's FlowCoeff record for the named flow
		pc := np.FlowCoeff[flwID]
		pc.RhoVec = rhoVec
		np.FlowCoeff[flwID] = pc
	}

	// create the network message to be introduced into the network.  
	// 
	nm := NetworkMsg{Route: route, SrvRate: np.AcceptedRate[flowID], PrArrvl: 1.0, MsgLen: msgLen,
		Connection: *connDesc, ConnectID: connectID, FlowID: flowID, 
			Msg: msg, NumPckts: 1, StartTime: evtMgr.CurrentSeconds()}

	// depending on the connLatency we post a message immediately, 
	// after an approximated delay, or through simulation

	latency := np.ComputeFlowLatency(&nm)

	np.SendNetMsg(evtMgr, &nm, 0.0)

	// if this is End, remove the identified flow
	if flowAction == End {
		np.RmFlow(evtMgr, flowID, route, latency)	
	}

	// for each changed flow report back the change and the acception rate, if requested
	for flwID := range chgFlowIDs {
		// probably not needed but cheap protection against changes in EstablishFlowRate
		if flwID == flowID {
			continue
		}
		np.ReportFlowChg(evtMgr, flwID, -1, flowAction, latency)
	}
}


// ReportFlowChg visits the return record maps to see if the named flow
// asked to have changes reported, and if so does so as requested.  The reports
// are schedule to occur 'latency' time in the future, when the effect of
// the triggered action is recognized at the triggering flow's receiving end.
func (np *NetworkPortal) ReportFlowChg(evtMgr *evtm.EventManager, flowID int, 
		action FlowAction, latency float64) {
	var rrec *rtnRecord
	var present bool
	var acceptedRate float64

	acceptedRate = np.AcceptedRate[flowID]
	rrec, present = np.ReportRtnSrc[flowID]

	// a request for reporting back to the source is indicated by the presence
	// of an entry in the ReportRtnSrc map
	if present {	
		rfs := new(RprtRate)
		rfs.FlowID = flowID
		rfs.AcceptedRate = acceptedRate
		rfs.Action = action

		// schedule notice of the acception rate
		evtMgr.Schedule(rrec.rtnCxt, rfs, rrec.rtnFunc, vrtime.SecondsToTime(latency))
	}

	rrec, present = np.ReportRtnDst[flowID]

	// if requested (by placement of a record in np.ReportRtnDst)
	// for a report to the destination
	if present {	
		rfs := new(RprtRate)
		rfs.FlowID = flowID
		rfs.AcceptedRate = acceptedRate
		rfs.Action = action

		// schedule notice of the acception rate
		evtMgr.Schedule(rrec.rtnCxt, rfs, rrec.rtnFunc, vrtime.SecondsToTime(latency))
	}
}

// BuildFlow establishes data structures in the interfaces and networks crossed
// by the given route, with a flow having the given flowID.
// No rate information is passed or set, other than initialization
func BuildFlow(flowID int, classID int, route *[]intrfcsToDev) PerfCoeff {

	// remember the performance coefficients for 'Place' latency, when requested
	var pc PerfCoeff
	pc.RhoVec = make([]float64,0)
		
	// for every stop on the route
	for idx:=0; idx<len((*route)); idx++ {

		// remember the step particulars, for later reference
		rtStep := (*route)[idx]
	
		// rtStep describes a path across a network.
		// the srcIntrfcID is the egress interface on the device that holds
		// that interface. rtStep.netID is the network it faces and
		// devID is the device on the other side.
		//
		egressIntrfc := IntrfcByID[rtStep.srcIntrfcID]
		egressIntrfc.AddFlow(flowID, classID, false)

		// adjust coefficients for embedded packet latency calculation.
		// Add the constant delay through the interface for every frame
		pc.AggConst += egressIntrfc.State.Delay

		// if the interface connection is a cable include the interface latency,
		// otherwise view the network step like an interface where
		// queueing occurs
		if egressIntrfc.Cable != nil {
			pc.AggConst += egressIntrfc.State.Latency
		} else {
			pc.AggConst += egressIntrfc.Faces.NetState.Latency
		}

		// the device gets a Forward entry for this flowID only if the flow doesn't 
		// originate there
		if idx>0 {

			// For idx > 0 we get the dstIntrfcID of (*route)[idx-1] for
			// the ingress interface
			ingressIntrfc := IntrfcByID[(*route)[idx-1].dstIntrfcID]
			ingressIntrfc.AddFlow(flowID, classID, true)

			pc.AggConst += ingressIntrfc.State.Delay
		
			dev := ingressIntrfc.Device

			// a device's forward entry for a flow associates the interface which admits the flow
			// with the interface that exits the flow.
			//   The information needed for such an entry comes from two route steps.
			// With idx>0 and idx < len(*route)-1 we know that the destination of the idx-1 route step
			// is the device ingress, and the source of the current route is the destination
			if idx < len(*route)-1 {
				ip := intrfcIDPair{prevID: ingressIntrfc.Number , nextID: (*route)[idx].srcIntrfcID}

				// remember the connection from ingress to egress interface in the device (router or switch)
				if dev.DevType() == RouterCode {
					rtr := dev.(*routerDev)
					rtr.addForward(flowID, classID, ip)

				} else if dev.DevType() == SwitchCode {
					swtch := dev.(*switchDev)
					swtch.addForward(flowID, classID, ip)
				}
			}
		}

		// remember the connection from ingress to egress interface in the network
		net := NetworkByID[rtStep.netID]
		ifcpr := intrfcIDPair{prevID: rtStep.srcIntrfcID, nextID: rtStep.dstIntrfcID}	
		net.AddFlow(flowID, classID, ifcpr)
	}
	return pc
}


// RemoveFlow de-establishes data structures in the interfaces and networks crossed
// by the given route, with a flow having the given flowID
func (np *NetworkPortal) RmFlow(evtMgr *evtm.EventManager, rmflowID int, 
		route *[]intrfcsToDev, latency float64) {
	var dev TopoDev

	// clear the request rate in case of reference before this call completes
	np.RequestRate[rmflowID] = 0.0

	// remove the flow from the data structures of the interfaces, devices, and networks
	// along the route
	for idx:=0; idx<len((*route)); idx++ {
		rtStep := (*route)[idx]
		var egressIntrfc *intrfcStruct
		var ingressIntrfc *intrfcStruct

		// all steps have an egress side.
		// get the interface
		egressIntrfc = IntrfcByID[rtStep.srcIntrfcID]
		dev = egressIntrfc.Device

		// remove the flow from the interface
		egressIntrfc.RmFlow(rmflowID, false)

		// adjust the network to the flow departure
		net := NetworkByID[rtStep.netID]
		ifcpr := intrfcIDPair{prevID: rtStep.srcIntrfcID, nextID: rtStep.dstIntrfcID}
		net.RmFlow(rmflowID, ifcpr)
	
		// the device got a Forward entry for this flowID only if the flow doesn't 
		// originate there
		if idx>0 {
			ingressIntrfc = IntrfcByID[(*route)[idx-1].dstIntrfcID]
			ingressIntrfc.RmFlow(rmflowID, true)

			// remove the flow from the device's forward maps
			if egressIntrfc.DevType == RouterCode {
				rtr := dev.(*routerDev)
				rtr.rmForward(rmflowID)
			} else if egressIntrfc.DevType == SwitchCode {
				swtch := dev.(*switchDev)
				swtch.rmForward(rmflowID)
			}
		}
	}


	// report the change to src and dst if requested
	np.ReportFlowChg(evtMgr, rmflowID, -1, End, latency)

	// clear up the maps with indices equal to the ID of the removed flow
	np.ClearRates(rmflowID)
	np.ClearConn(rmflowID)	
}

// EstablishFlowRate is given a major flow ID, request rate, and a route,
// and then first figures out what the accepted rate can be given the current state
// of all the major flows (by calling DiscoverFlowRate).   It follows up
// by calling SetFlowRate to establish that rate through the route for the named flow.
// Because of congestion, it may be that setting the rate may force recalculation of the
// rates for other major flows, and so SetFlowRate returns a map of flows to be
// revisited, and upper bounds on what their accept rates might be.  This leads to
// a recursive call to EstabishFlowRate
//
func (np *NetworkPortal) EstablishFlowRate(evtMgr *evtm.EventManager, flowID int, 
		requestRate float64, reservation bool, route *[]intrfcsToDev, action FlowAction) map[int]bool {

	var flowIDs map[int]bool = make(map[int]bool)

	// if the reservation is set we have already 
	// passed a test on the requested rate, and it will be accepted

	// start off with the asking rate
	acceptRate := requestRate

	// what rate can be sustained for this major flow?
	if action == End {
		acceptRate = 0.0
	}

	if !reservation {
		// but correct the accepted rate when the calculation calls for it
		acceptRate = np.DiscoverFlowRate(flowID, requestRate, reservation, route)
	}

	// set the rate, and get back a list of ids of major flows whose rates should be recomputed
	changes := np.SetFlowRate(evtMgr, flowID, acceptRate, reservation, route, action)

	// we'll keep track of all the flows calculated (or recalculated)
	flowIDs[flowID] = true

	// revisit every major flow whose converged rate might be affected by the rate setting in flow flowID
	for nxtID, nxtRate := range changes {
		if nxtID==flowID {
			continue
		}
		moreIDs := np.EstablishFlowRate(evtMgr, nxtID, 
			math.Min(nxtRate, np.RequestRate[nxtID]), reservation, route, action)
		flowIDs[nxtID] = true
		for mID := range moreIDs {
			flowIDs[mID] = true
		}
	}
	return flowIDs
}


// DiscoverFlowRates is called after the infrastructure for new 
// flow with ID flowID is set up, to determine what its rate will be 
func (np *NetworkPortal) DiscoverFlowRate(flowID int, 
		requestRate float64, reservation bool, route *[]intrfcsToDev) float64 {

	// minRate will end up with the minimum reservation along the flow's route
	minRate := requestRate 

	// visit each step on the route
	for idx:=0; idx<len((*route)); idx++ {

		rtStep := (*route)[idx]

		// flag indicating whether we need to analyze the ingress side of the route step.
		// The egress side is always analyzed
		doIngressSide := (idx>0)

		// ingress side first, then egress side
		for sideIdx:=0; sideIdx<2; sideIdx++ {
			ingressSide := (sideIdx==0)
			// the analysis looks the same for the ingress and egress sides, so
			// the same code block can be used for it.   Skip a side that is not
			// consistent with the route step
			if (ingressSide && !doIngressSide) {
				continue
			}

			// set up intrfc and depending on which interface side we're analyzing
			var intrfc *intrfcStruct
			var intrfcMap map[int]float64
			if ingressSide {
				// router steps describe interface pairs across a network,
				// so our ingress interface ID is the destination interface ID
				// of the previous routing step
				intrfc = IntrfcByID[(*route)[idx-1].dstIntrfcID]
				intrfcMap = intrfc.State.ToIngress
			} else {
				intrfc = IntrfcByID[(*route)[idx].srcIntrfcID]
				intrfcMap = intrfc.State.ToEgress
			}

			// the minimum rate cannot exceed the unreserved bandwidth of the interface
			minRate = math.Min(minRate, intrfc.State.OpenBndwdth)

			// toMap will hold the flow IDs of all unreserved major flows that are presented to the interface
			toMap := []int{}
			if !reservation {
				toMap = append(toMap, flowID)
			}

			for flwID, _ := range intrfcMap {
				// avoid having flowID in more than once
				if flwID == flowID {
					continue
				}
				// flow flwID is reserved we don't include it in toMap
				_, present := np.Rsrvd[flwID]
				if !present {
					toMap = append(toMap, flwID)	
				}
			}

			// for each unreserved flow compute the relative accepted flow rate relative
			// to the sum of all the accepted flow rates of unreserved flows through the interface.
			// The flow of interest, 
			if len(toMap) > 0 {
				rsrvdFracVec := ActivePortal.requestedLoadFracVec(toMap)
				minRate = math.Min(minRate, rsrvdFracVec[0]*intrfc.State.OpenBndwdth) 
			}

			// when focused on the egress side consider the network faced by the interface
			if !ingressSide {		
				net := intrfc.Faces

				// get a pointer to the interface on the other side of the network
				nxtIntrfc := IntrfcByID[rtStep.dstIntrfcID]

				// get identities of flows that share interfaces on either side of network
				toMap = []int{}
				if !reservation {
					toMap = append(toMap, flowID)
				}
				for flwID := range intrfc.State.ThruEgress{
					if flwID==flowID {
						continue
					}
					_, present := np.Rsrvd[flwID]
					if !present {
						toMap = append(toMap, flwID)
					}
				}
				for flwID := range nxtIntrfc.State.ToIngress{
					if slices.Contains(toMap, flwID) {
						continue
					}
					_, present := np.Rsrvd[flwID]
					if !present {
						toMap = append(toMap, flwID)
					}
				}
				// get the relative ask fraction among these of flowID
				if len(toMap) > 0 {
					rsrvdFracVec := ActivePortal.requestedLoadFracVec(toMap)

					// imagine that the network balances bandwidth allocation 
					// so that we can expect rsrvdFracVec[0]*net.NetState.OpenBndwdth for flowID
					minRate = math.Min(minRate, rsrvdFracVec[0]*net.NetState.OpenBndwdth) 
				}
			}
		}
	}
	return minRate
}

// SetFlowRate sets the accept rate for major flow flowID all along its path,
// and notes the identities of major flows which need attention because this change
// may impact them or other flows they interact with
func (np *NetworkPortal) SetFlowRate(evtMgr *evtm.EventManager, flowID int, acceptRate float64, classID int,
		route *[]intrfcsToDev, action FlowAction) map[int]float64  {

	// this is for keeps (for now...)
	np.AcceptedRate[flowID] = acceptRate

	// remember the ID of the major flows whose accepted rates may change
	changes := make(map[int]float64)

	// visit each step on the route
	for idx:=0; idx<len((*route)); idx++ {

		// remember the step particulars
		rtStep := (*route)[idx]

		// ifcpr may be needed to index into a map later
		ifcpr := intrfcIDPair{prevID: rtStep.srcIntrfcID, nextID: rtStep.dstIntrfcID}

		// flag indicating whether we need to analyze the ingress side of the route step.
		// The egress side is always analyzed
		doIngressSide := (idx>0)

		// ingress side first, then egress side
		for sideIdx:=0; sideIdx<2; sideIdx++ {
			ingressSide := (sideIdx==0)
			// the analysis looks the same for the ingress and egress sides, so
			// the same code block can be used for it.   Skip a side that is not
			// consistent with the route step
			if (ingressSide && !doIngressSide) {
				continue
			}

			// set up intrfc and intrfcMap depending on which interface side we're analyzing
			var intrfc *intrfcStruct
			var intrfcMap map[int]float64
			if ingressSide {
				// router steps describe interface pairs across a network,
				// so our ingress interface ID is the destination interface ID
				// of the previous routing step
				intrfc = IntrfcByID[(*route)[idx-1].dstIntrfcID]
				intrfcMap = intrfc.State.ToIngress
			} else {
				intrfc = IntrfcByID[(*route)[idx].srcIntrfcID]
				intrfcMap = intrfc.State.ToEgress
			}
	
			// if the accept rate hasn't changed coming into this interface,
			// we can skip it
			if math.Abs(acceptRate-intrfcMap[flowID]) < 1e-3 {
				continue
			}

			// if the interface wasn't congested before the change
			// or after the change, its peers aren't needing attention due to this interface
			wasCongested := intrfc.IsCongested(ingressSide)
			intrfc.ChgFlowRate(flowID, classID, acceptRate, ingressSide) 
			isCongested := intrfc.IsCongested(ingressSide)

			if (wasCongested || isCongested) { 
				toMap := []int{}
				if !reservation {
					toMap = append(toMap, flowID)
				}
				for flwID, _ := range intrfcMap {
					// avoid having flowID in more than once
					if flwID == flowID {
						continue
					}
					_, present := np.Rsrvd[flwID]
					if !present {
						toMap = append(toMap, flwID)	
					}
				}

				var rsrvdFracVec []float64
				if len(toMap) > 0 {
					rsrvdFracVec = np.requestedLoadFracVec(toMap)
				}

				for idx, flwID := range toMap {
					if flwID == flowID {
						continue
					}
					rsvdRate := rsrvdFracVec[idx]*intrfc.State.OpenBndwdth

					// remember the least bandwidth upper bound for major flow flwID
					chgRate, present := changes[flwID]
					if present {
						chgRate = math.Min(chgRate, rsvdRate)
						changes[flwID] = chgRate
					} else {
						changes[flwID] = rsvdRate
					}
				}
			}

			// for the egress side consider the network 
			if !ingressSide {	
				nxtIntrfc := IntrfcByID[rtStep.dstIntrfcID]

				net := intrfc.Faces

				if reservation {
					// see if this flow is already represented in the network
					_, present := net.NetState.Rsrvd[flowID]
					if !present {
						// remember the first time
						net.NetState.Rsrvd[flowID] = acceptRate
						net.NetState.OpenBndwdth -= acceptRate
					}
				} 

				wasCongested := net.IsCongested(intrfc, nxtIntrfc)
				net.ChgFlowRate(flowID, ifcpr, acceptRate)
				isCongested := net.IsCongested(intrfc, nxtIntrfc)

				if wasCongested || isCongested {
					toMap := []int{}
					if !reservation {
						toMap = append(toMap, flowID)
					} 
					for flwID, _ := range intrfc.State.ThruEgress {
						if flwID == flowID {
							continue
						}
						_, present := np.Rsrvd[flwID]
						if !present {
							toMap = append(toMap, flwID)
						}
					}
					nxtIntrfc := IntrfcByID[rtStep.dstIntrfcID]
					for flwID, _ := range nxtIntrfc.State.ToIngress {
						if slices.Contains(toMap, flwID) {
							continue
						}
						_, present := np.Rsrvd[flwID]
						if !present {
							toMap = append(toMap, flwID)
						}
					}
					var rsrvdFracVec []float64
					if len(toMap) > 0 {
						rsrvdFracVec = np.requestedLoadFracVec(toMap)
					}

					for idx, flwID := range toMap {
						if flwID == flowID {
							continue
						}
						rsvdRate := rsrvdFracVec[idx]*net.NetState.OpenBndwdth
						chgRate, present := changes[flwID]
						if present {
							chgRate = math.Min(chgRate, rsvdRate)
							changes[flwID] = chgRate
						} else {
							changes[flwID] = rsvdRate
						}
					}
				}
			}
		}
	}
	return changes
}

// ComputeRhoVec computes a data structure for the named major flow that 
// holds the utilization for every interface and network
// along the path, with the given acceptRate being the 'arrival rate' and the bandwidth available
// at an interface or network being the service rate
//   Another role played by ComputeRhoVec is to recompute the total ingress and egress loads on
// interfaces, because at the time ComputeRhoVec is called all the changes induced by a rate change
// have settled 
func (np *NetworkPortal) ComputeRhoVec(flowID int, 
		acceptRate float64, route *[]intrfcsToDev) []float64 {

	rhoVec := []float64{}

	// working variables
	var intrfcLoad float64
	var intrfc *intrfcStruct

	// for every stop on the route
	for idx:=0; idx<len((*route)); idx++ {

		// remember the particulars of this step in the route
		rtStep := (*route)[idx]

		// flag indicating whether we need to analyze the ingress side of the route step.
		// The egress side is always analyzed
		doIngressSide := (idx>0)

		// ingress side first, then egress side
		for sideIdx:=0; sideIdx<2; sideIdx++ {
			ingressSide := (sideIdx==0)
			// the analysis looks the same for the ingress and egress sides, so
			// the same code block can be used for it.   Skip a side that is not
			// consistent with the route step
			if (ingressSide && !doIngressSide) {
				continue
			}

			// set up intrfc depending on which interface side we're analyzing
			if ingressSide {
				// router steps describe interface pairs across a network,
				// so our ingress interface ID is the destination interface ID
				// of the previous routing step
				intrfc = IntrfcByID[(*route)[idx-1].dstIntrfcID]

				// compute the total load presented to the ingress side of the interface,
				// and save it 
				intrfcLoad = float64(0.0)
				for _, load := range intrfc.State.ToIngress {
					intrfcLoad += load
				}
				intrfc.State.IngressLoad = intrfcLoad
			} else {
				intrfc = IntrfcByID[(*route)[idx].srcIntrfcID]

				// compute the total load presented to the egress side of the interface,
				// and save it 
				intrfcLoad = float64(0.0)
				for _, load := range intrfc.State.ToEgress {
					intrfcLoad += load
				}
				intrfc.State.EgressLoad = intrfcLoad
			}

			// compute the relative utilization of the accepted rate to the bandwidth available to it,
			// assumed to be the total bandwidth on this side, less the sum of rates of other flows 
			// passing through the interface.
			// Notice that intrfcLoad is carried in from the apppropriate loop above
			util := acceptRate/(intrfc.State.OpenBndwdth-intrfcLoad+acceptRate)
			rhoVec = append(rhoVec, util)
	
			if !ingressSide {
				net := NetworkByID[rtStep.netID]
				//egressUtil := acceptRate/(net.NetState.OpenBndwdth-intrfc.State.EgressLoad)
				egressUtil := acceptRate/net.NetState.Bndwdth
				nxtIntrfc := IntrfcByID[rtStep.dstIntrfcID]
				load := intrfc.State.EgressLoad + nxtIntrfc.State.IngressLoad
				ingressUtil := acceptRate/(net.NetState.OpenBndwdth - load + acceptRate )
				util = math.Max(egressUtil, ingressUtil)
				rhoVec = append(rhoVec, util)
			}
		}
	}
	return rhoVec
}

// SendNetMsg moves a NetworkMsg, depending on the latency model.
// If 'Zero' the message goes to the destination instantly, with zero network latency modeled
// If 'Place' the message is placed at the destinatin after computing a delay timing through the network
// If 'Simulate' the message is placed at the egress port of the sending device and the message is simulated
// going through the network to its destination
//
func (np *NetworkPortal) SendNetMsg(evtMgr *evtm.EventManager, nm *NetworkMsg, offset float64) {

	// remember the latency model, and the route
	connLatency := nm.Connection.Latency
	route := nm.Route

	switch connLatency {
		case Zero:
			// the message's position in the route list---the last step
			nm.StepIdx = len(*route)-1
			np.SendImmediate(evtMgr, nm)
		case Place:
			// the message's position in the route list---the last step
			nm.StepIdx = len(*route)-1
			np.PlaceNetMsg(evtMgr, nm, offset)
		case Simulate:
			// get the interface at the first step
			intrfc := IntrfcByID[(*route)[0].srcIntrfcID]

			// schedule exit from first interface after msg passes through
			evtMgr.Schedule(intrfc, *nm, enterEgressIntrfc, vrtime.SecondsToTime(offset))
	}
}


// SendImmediate schedules the message with zero latency
func (np *NetworkPortal) SendImmediate(evtMgr *evtm.EventManager, nm *NetworkMsg) {

	// schedule exit from final interface after msg passes through
	ingressIntrfcID := (*nm.Route)[len(*nm.Route)-1].dstIntrfcID
	ingressIntrfc := IntrfcByID[ingressIntrfcID]
	evtMgr.Schedule(ingressIntrfc, *nm, exitIngressIntrfc, vrtime.SecondsToTime(0.0))
}

// PlaceNetMsg schedules the receipt of the message some deterministic time in the future,
// without going through the details of the intervening network structure
func (np *NetworkPortal) PlaceNetMsg(evtMgr *evtm.EventManager, nm *NetworkMsg, offset float64) {

	// get the ingress interface at the end of the route
	ingressIntrfcID := (*nm.Route)[len(*nm.Route)-1].dstIntrfcID
	ingressIntrfc := IntrfcByID[ingressIntrfcID]

	// compute the time through the network if simulated _now_ (and with no packets ahead in queue)
	latency := np.ComputeLatency(nm)

	// mark the message to indicate arrival at the destination
	nm.StepIdx = len((*nm.Route))-1

	// schedule exit from final interface after msg passes through
	evtMgr.Schedule(ingressIntrfc, *nm, exitIngressIntrfc, vrtime.SecondsToTime(latency+offset))
	return
}

// ComputeLatency approximates the latency from source to destination if compute now,
// with the state of the network frozen and no packets queued up
func (np *NetworkPortal) ComputeFlowLatency(nm *NetworkMsg) float64 {

	latencyType := nm.Connection.Latency
	if latencyType == Zero {
		return 0.0
	}

	connType := nm.Connection.Type
	flowID  := nm.FlowID

	route    := nm.Route
	
	frameSize := 1500
	if nm.MsgLen < frameSize {
		frameSize = nm.MsgLen
	}

	if connType == FlowConn {
		// value of input variable offset will be zero, and is irrelevant
		accept := np.AcceptedRate[flowID]
			
		// initialize latency with all the constants on the path
		latency := np.FlowCoeff[flowID].AggConst

		// recover the queuing utilizations
		rhoVec := np.FlowCoeff[flowID].RhoVec

		// use M/D/1 mean time in system for each queuing stop to estimate
		// time through the interfaces and networks. Implicit in use of rhoVec
		// is that the leading edge of the major flow gets the bandwidth allocation
		// of the flow
		for idx:=0; idx< len((*route)); idx++ {
			rtStep := (*route)[idx]
			srcIntrfc := IntrfcByID[rtStep.srcIntrfcID]
			dstIntrfc := IntrfcByID[rtStep.dstIntrfcID]
			net := srcIntrfc.Faces
			offsetIdx := 3*idx

			// use rhoVec in the order created
			latency += EstMD1Latency(rhoVec[offsetIdx],   frameSize, srcIntrfc.Bndwdth)
			latency += EstMD1Latency(rhoVec[offsetIdx+1], frameSize, dstIntrfc.Bndwdth)
			latency += EstMD1Latency(rhoVec[offsetIdx+2], frameSize, net.Bndwdth)
		}
		return latency
	}

	// compute the estimated mean time of transfer.
	// Start off with the constants
	latency := np.FlowCoeff[flowID].AggConst

	frameBits := frameSize*8
	var transIntrfc float64
	var maxTrans float64 
	var bndwdth float64
	
	route := nm.Route

	// add the time through every interface on the route
	for idx:=0; idx<len((*route)); idx++ {
		rtStep := (*route)[idx]
	
		if idx>0 {
			ingressIntrfc := IntrfcByID[(*route)[idx-1].dstIntrfcID]

			// get the bandwidth available, which will be the overall interface bandwidth,
			// minus the total interface Load, plus the rate just established
			bndwdth = ingressIntrfc.ServiceRate(nm, true)
			transIntrfc = float64(frameBits)/bndwdth
			maxTrans = math.Max(maxTrans, transIntrfc)
			latency += transIntrfc
		}

		egressIntrfc := IntrfcByID[rtStep.srcIntrfcID]
		bndwdth = egressIntrfc.ServiceRate(nm, false)
		transIntrfc = float64(frameBits)/bndwdth
		maxTrans = math.Max(maxTrans, transIntrfc)
		latency += transIntrfc

		net := NetworkByID[rtStep.netID]
		bndwdth = net.ServiceRate(nm)
		transIntrfc = float64(frameBits)/bndwdth
		maxTrans = math.Max(maxTrans, transIntrfc)

		latency += transIntrfc
	}
	return latency
}

func EstMM1Latency(bitRate, rho float64, msgLen int) float64 {
	// mean time in system for M/M/1 queue is
	// 1/(mu - lambda)
	// in units of pckts/sec.
	// Now
	//
	// bitRate/(msgLen*8) = lambda
	//
	// and rho = lambda/mu
	//	
	// so mu = lambda/rho
	// and (mu-lambda) = lambda*(1.0/rho - 1.0)
	// and mean time in system is
	//
	// 1.0/(lambda*(1/rho - 1.0))
	// 
	if math.Abs(1.0-rho) < 1e-3 {
		// force rho to be 95%
		rho = 0.95
	}
	lambda := bitRate/float64(msgLen)
	denom := lambda*(1.0/rho - 1.0)
	return 1.0/denom
}

//func EstMD1Latency(bitRate, rho float64, msgLen int) float64 {
func EstMD1Latency(rho float64, msgLen int, bndwdth float64) float64 {
	// mean time in waiting for service in M/D/1 queue is
	//  1/mu +  rho/(2*mu*(1-rho))
	//	
	mu := bndwdth/(float64(msgLen*8)/1e6)
	imu := 1.0/mu

	if math.Abs(1.0-rho) < 1e-3 {
		// if rho too large, force it to be 99%
		rho = 0.99
	}
	denom := 2*mu*(1.0-rho)
	return imu + rho/denom
}


