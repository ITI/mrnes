package mrnes

import (
	"fmt"
	"github.com/iti/evt/evtm"
	"github.com/iti/evt/vrtime"
	"golang.org/x/exp/slices"
)

type Flow struct {
	ExecID        int
	FlowID        int
	ConnectID     int
	Number        int
	Name          string
	Mode		  string
	FlowModel	  string
	Elastic       bool		   
	Pckt          bool		   
	Src           string
	Dst           string
	FrameSize	  int
	SrcID         int
	DstID         int
	Groups        []string
	RequestedRate float64
	AcceptedRate  float64
	RtnDesc       RtnDesc
	Suspended     bool
}

func (bgf *Flow) matchParam(attrbName, attrbValue string) bool {
	switch attrbName {
	case "name":
		return bgf.Name == attrbValue
	case "group":
		return slices.Contains(bgf.Groups, attrbValue)
	case "srcdev":
		return bgf.Src == attrbValue
	case "dstdev":
		return bgf.Dst == attrbValue
	}
	return false
}

func (bgf *Flow) paramObjName() string {
	return bgf.Name
}

func (bgf *Flow) setParam(paramType string, value valueStruct) {
	switch paramType {
	case "reqrate":
		bgf.RequestedRate = value.floatValue
	case "mode":
		bgf.Mode = value.stringValue
		bgf.Elastic = (value.stringValue == "elastic-flow")
		bgf.Pckt    = (value.stringValue == "packet") || (value.stringValue == "pckt") || (value.stringValue == "pcket")
	case "flowmodel":
		bgf.FlowModel = value.stringValue
	}
}

func (bgf *Flow) LogNetEvent(time vrtime.Time, msg *NetworkMsg, desc string) {
}

var FlowList map[int]*Flow

func InitFlowList() {
	FlowList = make(map[int]*Flow)
}

func CreateFlow(srcDev string, dstDev string,
	requestRate float64, frameSize int, mode, flowmodel string, 
		execID int, groups []string) *Flow {

	if !(requestRate > 0) {
		return nil
	}

	bgf := new(Flow)
	bgf.RequestedRate = requestRate
	numberOfFlows += 1
	bgf.FlowID = numberOfFlows
	bgf.Src = srcDev
	bgf.SrcID = EndptDevByName[srcDev].DevID()
	bgf.Dst = dstDev
	bgf.DstID = EndptDevByName[dstDev].DevID()
	bgf.ExecID = execID
	bgf.Number = nxtID()
	bgf.ConnectID = 0 // indicating absence
	bgf.Mode = mode
	bgf.FlowModel = flowmodel
	bgf.FrameSize = frameSize
	bgf.Elastic = (mode=="elastic-flow")
	bgf.Pckt    = (mode=="packet") || (mode=="pckt") || (mode=="pcket")
	bgf.Suspended = false
	copy(bgf.Groups, groups)

	FlowList[bgf.FlowID] = bgf

	return bgf
}

func bgfPcktArrivals(evtMgr *evtm.EventManager, context any, data any) any {
	// acquire the flowID
	flowID := context.(int)
	bgf := FlowList[flowID]

	// if the flow is suspended just leave
	if bgf.Suspended {
		return nil
	}

	endptDev, present := EndptDevByName[bgf.Src]
	if !present {
		panic(fmt.Errorf("%s not the name of an endpoint", bgf.Src))
	}

	rng := endptDev.DevRng()
	arrivalRatePckts := bgf.RequestedRate*1e6/(float64(8*bgf.FrameSize))
	var interarrival float64	
	switch bgf.FlowModel {

		case "expon","exp","exponential":
			u01 := rng.RandU01()
			params := []float64{arrivalRatePckts}
			interarrival = sampleExpRV(u01, params)
			
		case "const","constant":
			interarrival = 1.0/arrivalRatePckts
	}
	// schedule the next arrival
	evtMgr.Schedule(context, data, bgfPcktArrivals, vrtime.SecondsToTime(interarrival))

	// enter the network after the first pass through (which happens at time 0.0)
	if evtMgr.CurrentSeconds() > 0.0 {
		connDesc := ConnDesc{Type: DiscreteConn, Latency: Simulate, Action: None}
		IDs := NetMsgIDs{ExecID: bgf.ExecID, FlowID: flowID}

		// indicate where the returning event is to be delivered

		// need something new here
		rtnDesc := new(RtnDesc)
		rtnDesc.Cxt = nil

		// indicate what to do if there is a packet loss
		lossDesc := new(RtnDesc)
		lossDesc.Cxt = nil

		rtns := RtnDescs{Rtn: rtnDesc, Src: nil, Dst: nil, Loss: lossDesc}


		ActivePortal.EnterNetwork(evtMgr, bgf.Src, bgf.Dst, bgf.FrameSize, 
			&connDesc, IDs, rtns, arrivalRatePckts, 0, nil)
	}	
	return nil
}


// Start scheduled the beginning of the flow
func (bgf *Flow) StartFlow(evtMgr *evtm.EventManager, rtns RtnDescs) bool {
	// bgf.RtnDesc.Cxt = context
	// bgf.RtnDesc.EvtHdlr = hdlr
	bgf.ConnectID = 0 // indicating absence

	// rtnDesc := new(RtnDesc)
	// rtnDesc.Cxt = context
	// rtnDesc.EvtHdlr = hdlr

	ActivePortal.Mode[bgf.FlowID] = bgf.Mode
	ActivePortal.Elastic[bgf.FlowID] = bgf.Elastic
	ActivePortal.Pckt[bgf.FlowID] = bgf.Pckt

	// rtns := RtnDescs{Rtn: rtnDesc, Src: nil, Dst: nil, Loss: nil}

	OK := true
	if !bgf.Pckt {	
		connDesc := ConnDesc{Type: FlowConn, Latency: Zero, Action: Srt}
		IDs := NetMsgIDs{ExecID: bgf.ExecID, FlowID: bgf.FlowID}

		bgf.ConnectID, _, OK = ActivePortal.EnterNetwork(evtMgr, bgf.Src, bgf.Dst, 1560,
			&connDesc, IDs, rtns, bgf.RequestedRate, 0, nil)
	} else {
		evtMgr.Schedule(bgf.FlowID, nil, bgfPcktArrivals, vrtime.SecondsToTime(0.0))
	}

	return OK
}

func (bgf *Flow) RmFlow(evtMgr *evtm.EventManager, context any, hdlr evtm.EventHandlerFunction) {
	bgf.RtnDesc.Cxt = context
	bgf.RtnDesc.EvtHdlr = hdlr

	connDesc := ConnDesc{Type: FlowConn, Latency: Zero, Action: End}
	IDs := NetMsgIDs{ExecID: bgf.ExecID, FlowID: bgf.FlowID}

	rtnDesc := new(RtnDesc)
	rtnDesc.Cxt = context
	rtnDesc.EvtHdlr = hdlr

	rtns := RtnDescs{Rtn: rtnDesc, Src: nil, Dst: nil, Loss: nil}

	ActivePortal.EnterNetwork(evtMgr, bgf.Src, bgf.Dst, 1560, &connDesc, IDs, rtns, 0.0, 0, nil)
}

func (bgf *Flow) ChangeRate(evtMgr *evtm.EventManager, requestRate float64) bool {
	route := findRoute(bgf.SrcID, bgf.DstID)

	msg := new(NetworkMsg)
	msg.Connection = ConnDesc{Type: FlowConn, Latency: Zero, Action: Chg}
	msg.ExecID = bgf.ExecID
	msg.FlowID = bgf.FlowID
	msg.NetMsgType = FlowType
	msg.Rate = requestRate
	msg.MsgLen = bgf.FrameSize

	success := ActivePortal.FlowEntry(evtMgr, bgf.Src, bgf.Dst, msg.MsgLen, &msg.Connection,
		bgf.FlowID, bgf.ConnectID, requestRate, route, msg)

	return success
}

func FlowRateChange(evtMgr *evtm.EventManager, cxt any, data any) any {
	bgf := cxt.(*Flow)
	rprt := data.(*RtnMsgStruct)
	bgf.AcceptedRate = rprt.Rate
	return nil
}

func FlowRemoved(evtMgr *evtm.EventManager, cxt any, data any) any {
	bgf := cxt.(*Flow)
	delete(FlowList, bgf.FlowID)
	evtMgr.Schedule(bgf.RtnDesc.Cxt, data, bgf.RtnDesc.EvtHdlr, vrtime.SecondsToTime(0.0))

	return nil
}

func AcceptedFlowRate(evtMgr *evtm.EventManager, context any, data any) any {
	bgf := context.(*Flow)
	rprt := data.(*RtnMsgStruct)
	bgf.AcceptedRate = rprt.Rate
	evtMgr.Schedule(bgf.RtnDesc.Cxt, data, bgf.RtnDesc.EvtHdlr, vrtime.SecondsToTime(0.0))
	return nil
}

func StartFlows(evtMgr *evtm.EventManager) {
	ActivePortal = CreateNetworkPortal()

	rtnDesc := new(RtnDesc)
	rtnDesc.Cxt = nil
	rtnDesc.EvtHdlr = ReportFlowEvt
	rtns := RtnDescs{Rtn: rtnDesc, Src: rtnDesc, Dst: rtnDesc, Loss: nil}

	for _, bgf := range FlowList {
		success := bgf.StartFlow(evtMgr, rtns)
		if !success {
			fmt.Println("Flow " + bgf.Name + " failed to start")
		}
	}
}

func StopFlows(evtMgr *evtm.EventManager) {
	for _, bgf := range FlowList {
		bgf.Suspended = true	
	}
}

func ReportFlowEvt(evtMgr *evtm.EventManager, context any, data any) any {
	fmt.Println("Flow event")
	return nil
}
