package mrnes

import (
	"encoding/json"
	"github.com/iti/evt/vrtime"
	"gopkg.in/yaml.v3"
	"strconv"
	"path"
	"os"
)

type TraceRecordType int
const (
	NetworkType TraceRecordType = iota
	CmpPtnType
)

var trtToStr map[TraceRecordType]string = map[TraceRecordType]string{NetworkType:"network",CmpPtnType:"cp"}

type TraceInst struct {
	TraceTime string
	TraceType string
	TraceStr  string
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
	ExpName string			`json:"expname" yaml:"expname"`

	// text name associated with each objID
	NameByID map[int]NameType `json:"namebyid" yaml:"namebyid"`

	// all trace records for this experiment
	Traces map[int][]TraceInst	`json:"traces" yaml:"traces"`
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
	tm.Traces = make(map[int][]TraceInst) // traces have 'execution' origins, are saved by index to these
	return tm
}

// Active tells the caller whether the Trace Manager is actively being used
func (tm *TraceManager) Active() bool {
	return tm.InUse
}

// AddTrace creates a record of the trace using its calling arguments, and stores it
func (tm *TraceManager) AddTrace(vrt vrtime.Time, execID int, trace TraceInst) {

	// return if we aren't using the trace manager
	if !tm.InUse {
		return
	}

	_, present := tm.Traces[execID]
	if !present {
		tm.Traces[execID] = make([]TraceInst, 0)
	}
	tm.Traces[execID] = append(tm.Traces[execID], trace)
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
// NetTraceRecord saves information about the visitation of a message to some point in the simulation.
// saved for post-run analysis
type NetTrace struct {
	Time      float64 // time in float64
	Ticks     int64   // ticks variable of time
	Priority  int64   // priority field of time-stamp
	MajorID    int    // integer identifier identifying the chain of traces this is part of
	MinorID    int    // integer identifier identifying the chain of traces this is part of
	ConnectID int     // integer identifier of the network connection
	ObjID     int     // integer id for object being referenced
	Op        string  // "start", "stop", "enter", "exit"
	PcktIdx   int     // packet index inside of a multi-packet message
	Packet    bool    // true if the event marks the passage of a packet (rather than flow)
	MsgType   string
	Rate      float64 // rate associated with the connection
}

func (ntr *NetTrace) TraceType() TraceRecordType {
	return NetworkType
}

func (ntr *NetTrace) Serialize() string {
	var bytes []byte
	var merr error

	bytes, merr = yaml.Marshal(*ntr)

	if merr != nil {
		panic(merr)
	}
	return string(bytes[:])
}

// AddNetTrace creates a record of the trace using its calling arguments, and stores it
func AddNetTrace(tm *TraceManager, vrt vrtime.Time, nm *NetworkMsg, objID int, op string) {
	ntr := new(NetTrace)
	ntr.Time = vrt.Seconds()
	ntr.Ticks = vrt.Ticks()
	ntr.Priority = vrt.Pri()
	ntr.ConnectID = nm.ConnectID
	ntr.MajorID = nm.MajorID
	ntr.MinorID = nm.MinorID
	ntr.ObjID = objID
	ntr.Op = op
	ntr.PcktIdx = nm.PcktIdx
	ntr.MsgType = nmtToStr[nm.NetMsgType]

	ntrStr := ntr.Serialize()
	traceTime := strconv.FormatFloat(vrt.Seconds(), 'f', -1, 64)

	trcInst := TraceInst{TraceTime: traceTime, TraceType:"network", TraceStr:ntrStr}
	tm.AddTrace(vrt, ntr.MajorID, trcInst)
}

