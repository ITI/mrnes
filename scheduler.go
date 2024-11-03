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
	arrive		 float64				   // time of task arrival
	req          float64                   // required service
	key			 float64				   // key used for heap ordering
	pri		     float64				   // priority, the larger the number the greater the priority
	timeslice    float64                   // timeslice
	execID	     int
	devID        int
	completeFunc evtm.EventHandlerFunction // call when finished
	context      any                       // remember this from caller, to return when finished
	Msg          any                       // information package being carried
}

// unique identifier for each task
var nxtTaskIdx int = 0

// createTask is a constructor
func createTask(op string, arrive, req, pri, timeslice float64, msg any, context any, 
		execID, devID int, complete evtm.EventHandlerFunction) *Task {

	nxtTaskIdx += 1

	// if priority is zero, add 1.0	
	if !(pri > 0.0) {
		return &Task{OpType: op, arrive: arrive, req: req, pri: 1.0, 
			timeslice: timeslice, Msg: msg, context: context, execID: execID,
			devID: devID, completeFunc: complete}
	} 
	return &Task{OpType: op, req: req, pri: pri, timeslice: timeslice, Msg: msg, context: context, 
		execID: execID,  devID: devID, completeFunc: complete}
}

// reqSrvHeap and its methods implement a min-priority heap
// on the residual service requirements of tasks
type reqSrvHeap []*Task

func (h reqSrvHeap) Len() int           { return len(h) }
func (h reqSrvHeap) Less(i, j int) bool { return h[i].key < h[j].key }  // key is set by the heap 
func (h reqSrvHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }

func (h *reqSrvHeap) Push(x any) {
	*h = append(*h, x.(*Task))
}

/*
func (h *reqSrvHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[0 : n-1]
	return x
}
*/

func (h *reqSrvHeap) Pop() any {
	old := *h
	x := old[0]
	*h = old[1:]
	return x
}

// TaskScheduler holds data structures supporting the multi-core scheduling
type TaskScheduler struct {
	cores     int         // number of computational cores
	inBckgrnd int		  // number of cores being used for background traffic
	bckgrndOn bool		  // set to true when background processing is in use
	ts        float64     // default timeslice for cores
	waiting   []*Task     // work to do, not in service
	priWaiting reqSrvHeap // manage work being served concurrently
	inservice reqSrvHeap  // manage work being served concurrently
}

// CryptoScheduler holds data structures supporting the multi-core scheduling of crypto operations
type CryptoScheduler struct {
	EncryptScheduler *TaskScheduler
	DecryptScheduler *TaskScheduler
}

// CreateTaskScheduler is a constructor
func CreateTaskScheduler(cores int) *TaskScheduler {
	ts := new(TaskScheduler)
	ts.cores = cores
	ts.waiting = []*Task{}
	ts.inservice = []*Task{}
	heap.Init(&ts.inservice)
	heap.Init(&ts.priWaiting)
	return ts
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
func (ts *TaskScheduler) Schedule(evtMgr *evtm.EventManager, op string, req, pri, timeslice float64, 
	context any, msg any, execID, objID int, complete evtm.EventHandlerFunction) bool {

	AddSchedulerTrace(devTraceMgr, evtMgr.CurrentTime(), ts, execID, objID, "schedule["+op+"]") 

	// create the Task, and remember it
	now := evtMgr.CurrentSeconds()
	task := createTask(op, now, req, pri, timeslice, msg, context, execID, objID, complete)

	// either put into service or put in the waiting queue
	inservice := ts.joinQueue(evtMgr, task)

	// return flag indicating whether task was placed immediately into service
	return inservice
}

// joinQueue is called to put a Task into the data structure that governs
// allocation of service
func (ts *TaskScheduler) joinQueue(evtMgr *evtm.EventManager, task *Task) bool {
	// if all the cores are busy, put in the waiting queue and return

	if ts.cores <= ts.inservice.Len() + ts.inBckgrnd {
		now := evtMgr.CurrentSeconds()
		task.key = 1.0/(math.Pow(now-task.arrive,0.5)*task.pri)
		heap.Push(&ts.priWaiting, task)
		return false
	}

	execute := task.timeslice
	finished := false
	if task.req <= task.timeslice {
		execute = task.req
		finished = true
	}

	// schedule event handler for when this timeslice completes
	evtMgr.Schedule(ts, finished, timeSliceComplete, vrtime.SecondsToTime(execute))

	// if the non-background task is going to complete we can schedule the event handler for the end of task
	if finished {
		evtMgr.Schedule(task.context, task, task.completeFunc, vrtime.SecondsToTime(task.req))
	}

	task.req = math.Max(task.req-task.timeslice, 0.0)
	task.key = task.req				// least time first
	heap.Push(&ts.inservice, task)
	return finished
}

// timesliceComplete is called when the timeslice allocated to a task has completed
func timeSliceComplete(evtMgr *evtm.EventManager, context any, data any) any {
	ts := context.(*TaskScheduler)

	finished := data.(bool)

	// get first completing task of tasks in service
	taskAny := heap.Pop(&ts.inservice)
	task := taskAny.(*Task)

	AddSchedulerTrace(devTraceMgr, evtMgr.CurrentTime(), ts, task.execID, task.devID, "complete") 

	if ts.priWaiting.Len() > 0 {
		newtask := heap.Pop(&ts.priWaiting)
		ts.joinQueue(evtMgr, newtask.(*Task))
	}

	if finished {
		return nil
	}

	// task.req > 0.0 so schedule up another round of service
	ts.joinQueue(evtMgr, task)
	return nil
}

// add a background task to a scheduler, give length of burst
func addBckgrnd(evtMgr *evtm.EventManager, context any, data any) any {
	endpt := context.(*endptDev)	
	ts := endpt.EndptSched

	if !ts.bckgrndOn {
		return nil
	}


	// don't do anything if all the cores are busy
	if ts.inBckgrnd + ts.inservice.Len() < ts.cores {
		ts.inBckgrnd += 1

		u01 := u01List[endpt.EndptState.BckgrndIdx]
		endpt.EndptState.BckgrndIdx = (endpt.EndptState.BckgrndIdx+1)%10000

		duration := -endpt.EndptState.BckgrndSrv*math.Log(1.0-u01)
		// schedule the background task completion
		evtMgr.Schedule(endpt, nil, rmBckgrnd, vrtime.SecondsToTime(duration))
	}

	// schedule the next background arrival
	u01 := u01List[endpt.EndptState.BckgrndIdx]
	endpt.EndptState.BckgrndIdx = (endpt.EndptState.BckgrndIdx+1)%10000
	arrival := -math.Log(1.0-u01)/endpt.EndptState.BckgrndRate
	evtMgr.Schedule(endpt, nil, addBckgrnd, vrtime.SecondsToTime(arrival))
	return nil
}

// remove a background task
func rmBckgrnd(evtMgr *evtm.EventManager, context any, data any) any {
	endpt := context.(*endptDev)
	ts := endpt.EndptSched

	if !ts.bckgrndOn {
		return nil
	}

	if ts.inBckgrnd > 0 {
		ts.inBckgrnd -= 1
	}

	// if there are ordinary tasks in queue and enough cores now, free one up
	if ts.priWaiting.Len() > 0 && ts.inservice.Len() + ts.inBckgrnd < ts.cores {
		newtask := ts.priWaiting.Pop()
		ts.joinQueue(evtMgr, newtask.(*Task))
	}
	return nil
}
	
