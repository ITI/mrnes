package mrnes

import (
	"github.com/iti/evt/evtm"
	"github.com/iti/evt/vrtime"
)

type BckgrndFlow struct {
	ExecID int
	FlowID int
	ClassID int
	ConnectID int
	Elastic bool
	FlowType ConnType
	Src string
	Dst string
	RequestedRate float64
	AcceptedRate  float64
	RtnDesc RtnDesc
}

var BckgrndFlowList map[int]*BckgrndFlow = make(map[int]*BckgrndFlow)

func CreateBckgrndFlow(evtMgr *evtm.EventManager, srcDev string, dstDev string, 
		requestRate float64, elastic bool, execID int, flowID int, classID int, context any, 
			hdlr evtm.EventHandlerFunction) (*BckgrndFlow, bool) {
	
	if !(requestRate > 0) {
		return nil, false
	}

	bgf := new(BckgrndFlow)
	bgf.RequestedRate = requestRate
	bgf.Src = srcDev
	bgf.Dst = dstDev
	bgf.FlowID = flowID
	bgf.ExecID = execID
	bgf.ClassID = classID
	bgf.FlowType = FlowConn
	bgf.RtnDesc.Cxt = context
	bgf.RtnDesc.EvtHdlr = hdlr
	bgf.ConnectID = 0			// indicating absence
	bgf.Elastic = elastic
	ActivePortal.Elastic[flowID] = elastic

	connDesc := ConnDesc{Type: FlowConn, Latency: Zero, Action: Srt}
	IDs := NetMsgIDs{ExecID: execID, FlowID: bgf.FlowID, ClassID: classID}

	// msg.Populate(execID, bgf.FlowID, classID, requestRate, 1500, "srt")

	rtnDesc := new(RtnDesc)
	rtnDesc.Cxt = context
	rtnDesc.EvtHdlr = hdlr

	rtns := RtnDescs{Rtn: rtnDesc, Src: nil, Dst: nil, Loss: nil}	

	var OK bool

	bgf.ConnectID, _, OK = ActivePortal.EnterNetwork(evtMgr, srcDev, dstDev, 1500,
		&connDesc, IDs, rtns, requestRate, nil)

	if !OK {
		return nil, false
	}

	BckgrndFlowList[bgf.FlowID] = bgf

	return bgf, true	
}

func (bgf *BckgrndFlow) RmBckgrndFlow(evtMgr *evtm.EventManager, context any, hdlr evtm.EventHandlerFunction) {
	bgf.RtnDesc.Cxt = context
	bgf.RtnDesc.EvtHdlr = hdlr

	connDesc := ConnDesc{Type: FlowConn, Latency: Zero, Action: End}
	IDs := NetMsgIDs{ExecID: bgf.ExecID, FlowID: bgf.FlowID, ClassID: bgf.ClassID}

	// msg.Populate(bgf.ExecID, bgf.FlowID, bgf.ClassID, 0.0, 1500, "end")

	rtnDesc := new(RtnDesc)
	rtnDesc.Cxt = context
	rtnDesc.EvtHdlr = hdlr

	rtns := RtnDescs{Rtn: rtnDesc, Src: nil, Dst: nil, Loss: nil}	

	ActivePortal.EnterNetwork(evtMgr, bgf.Src, bgf.Dst, 1500, &connDesc, IDs, rtns, 0.0, nil)
}

func BckgrndFlowRateChange(evtMgr *evtm.EventManager, cxt any, data any) any {
	bgf := cxt.(*BckgrndFlow)
	rprt := data.(*RtnMsgStruct)
	bgf.AcceptedRate = rprt.Rate
	return nil
}
		 
func BckgrndFlowRemoved(evtMgr *evtm.EventManager, cxt any, data any) any {
	bgf := cxt.(*BckgrndFlow)
	delete(BckgrndFlowList, bgf.FlowID)	
	evtMgr.Schedule(bgf.RtnDesc.Cxt, data, bgf.RtnDesc.EvtHdlr, vrtime.SecondsToTime(0.0))

	return nil
}
		 
func AcceptedBckgrndFlowRate(evtMgr *evtm.EventManager, context any, data any) any {
	bgf := context.(*BckgrndFlow)
	rprt := data.(*RtnMsgStruct)
	bgf.AcceptedRate = rprt.Rate
	evtMgr.Schedule(bgf.RtnDesc.Cxt, data, bgf.RtnDesc.EvtHdlr, vrtime.SecondsToTime(0.0))
	return nil
}

