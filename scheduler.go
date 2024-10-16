package mrnes

// scheduler.go holds structs, methods and data structures that
// support scheduling of tasks, e.g., function executions, on resources
// that are limited

// When a task is scheduled the caller specifies how much service is required
// (in simulation time units), and a time-slice.   If the time-slice is larger the
// service, when given, is allocated all at once.   If the service requirement
// exceeds the time-slice the task is given the time-slice among of service, and the
// residual task is scheduled.    Allocation of core resources is first-come first-serve

import (
	"container/heap"
	"github.com/iti/evt/evtm"
	"github.com/iti/evt/vrtime"
	"math"
)

// Task describes the service requirements of an operation on a msg
type Task struct {
	OpType       string                    // what operation is being performed
	low			 bool
	req          float64                   // required service
	reqPri		 int64					   // priority for scheduling
	ts           float64                   // timeslice
	completeFunc evtm.EventHandlerFunction // call when finished
	context      any                       // remember this from caller, to return when finished
	Msg          any                       // information package being carried
}

// unique identifier for each task
var nxtTaskIdx int = 0

// createTask is a constructor
func createTask(op string, low bool, req, ts float64, pri int64, msg any, 
		context any, complete evtm.EventHandlerFunction) *Task {
	nxtTaskIdx += 1
	return &Task{OpType: op, low: low, req: req, ts: ts, reqPri: pri, Msg: msg, context: context, completeFunc: complete}
}

// reqSrvHeap and its methods implement a min-priority heap
// on the residual service requirements of tasks
type reqSrvHeap []*Task

func (h reqSrvHeap) Len() int           { return len(h) }
func (h reqSrvHeap) Less(i, j int) bool { return h[i].req < h[j].req }
func (h reqSrvHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }

func (h *reqSrvHeap) Push(x any) {
	*h = append(*h, x.(*Task))
}

func (h *reqSrvHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[0 : n-1]
	return x
}

// TaskScheduler holds data structures supporting the multi-core scheduling
type TaskScheduler struct {
	cores     int        // number of computational cores
	ts        float64    // default timeslice for cores
	waitingHigh []*Task    // work to do, not in service
	waitingLow  []*Task    // work to do, not in service
	inservice reqSrvHeap // manage work being served concurrently
}

// CryptoScheduler holds data structures supporting the multi-core scheduling of crypto operations
type CryptoScheduler struct {
	EncryptScheduler *TaskScheduler
	DecryptScheduler *TaskScheduler
}

// CreateTaskScheduler is a constructor
func CreateTaskScheduler(cores int) *TaskScheduler {
	ops := new(TaskScheduler)
	ops.cores = cores
	ops.waitingHigh = []*Task{}
	ops.waitingLow = []*Task{}
	ops.inservice = []*Task{}
	heap.Init(&ops.inservice)
	return ops
}

// CreateCryptoScheduler is a constructor
func CreateCryptoScheduler(cores int) *CryptoScheduler {
	cs := new(CryptoScheduler)
	cs.EncryptScheduler = CreateTaskScheduler(cores/2)
	cs.DecryptScheduler = CreateTaskScheduler(cores/2)
	return cs
}


// Schedule puts a piece of work either in queue to be done, or in service.  Parameters are
// - op : a code for the type of work being done
// - req : the service requirements for this task, on this computer
// - ts  : timeslice, the amount of service the task gets before yielding
// - msg : the message being processed
// - complete : an event handler to be called when the task has completed
// The return is true if the 'task is finished' event was scheduled.
func (ops *TaskScheduler) Schedule(evtMgr *evtm.EventManager, low bool, op string, req, ts float64, pri int64,
	context any, msg any, complete evtm.EventHandlerFunction) bool {

	// create the Task, and remember it
	task := createTask(op, low, req, ts, pri, msg, context, complete)

	// either put into service or put in the waiting queue
	inservice := ops.joinQueue(evtMgr, low, task)

	// return flag indicating whether task was placed immediately into service
	return inservice
}

// joinQueue is called to put a Task into the data structure that governs
// allocation of service
func (ops *TaskScheduler) joinQueue(evtMgr *evtm.EventManager, low bool, task *Task) bool {
	// if the service requirement is zero we're immediately done
	zeroSrv := (task.ts == 0)
	execute := task.ts
	var finished bool
	if !zeroSrv {
		// if all the cores are busy, put in the waiting queue and return
		if ops.cores <= len(ops.inservice) {
			if low {
				ops.waitingLow = append(ops.waitingLow, task)
			} else {
				ops.waitingHigh = append(ops.waitingHigh, task)
			}
			return false
		}
		
		finished = false
		if task.req <= task.ts {
			execute = task.req
			finished = true
		}
	} else {
		finished = true
	}

	// schedule event handler for when this timeslice completes
	execTime := vrtime.SecondsToTime(execute)
	if !zeroSrv {
		execTime.SetPri(task.reqPri)
		evtMgr.Schedule(ops, finished, timeSliceComplete, execTime)
	}

	// if the task is going to complete we can schedule the event handler for the end of task
	if finished {
		evtMgr.Schedule(task.context, task, task.completeFunc, vrtime.SecondsToTime(task.req))
	}

	if !zeroSrv {
		task.req = math.Max(task.req-task.ts, 0.0)
		heap.Push(&ops.inservice, task)
	}
	return finished
}

// timesliceComplete is called when the timeslice allocated to a task has completed
func timeSliceComplete(evtMgr *evtm.EventManager, context any, data any) any {
	ops := context.(*TaskScheduler)

	finished := data.(bool)

	// get first completing task of tasks in service
	taskAny := heap.Pop(&ops.inservice)
	task := taskAny.(*Task)

	// if the waiting queue is not empty we need to put its first (FCFS) member into service
	if len(ops.waitingHigh) > 0 {
		newtask := ops.waitingHigh[0]
		ops.waitingHigh = ops.waitingHigh[1:]
		ops.joinQueue(evtMgr, false, newtask)
	} else if len(ops.waitingLow) > 0 {
		newtask := ops.waitingLow[0]
		ops.waitingLow = ops.waitingLow[1:]
		ops.joinQueue(evtMgr, true, newtask)
	}

	if finished {
		return nil
	}

	// task.req > 0.0 so schedule up another round of service
	ops.joinQueue(evtMgr, task.low, task)
	return nil
}
