package mrnes

// flow-sim.go holds data structures and methods for
// the strmQueue structure, used to do fast packet instant
// simulations of flows through interfaces
import (
	"github.com/iti/rngstream"
	"math"
)

// strmQueue is the central data structure for managing strm flow advancement
type strmQueue struct {
	// only true if there is positive flow through the interface
	active bool

	// the simulation time the strmQueue has been advanced to
	time float64

	// time for one strm frame through the interface.  Computed as
	// a function of the interface bandwidth and frame length
	serviceTime float64

	frameLen   int       // number of bytes in a strm frame
	bndwdth    float64   // bandwidth of the interface
	strmRate   float64   // rate of strm frames being delivered to the interface
	completes  float64   // when the strmQueue is serving, the completion time of the frame in service
	serving    bool      // true when the strmQueue is service, false when an ordinary packet is in passage
	inQ        []float64 // queue of arrival times of as-yet-un-full-served strmQueue frames

	// function that computes inter-arrival times for strm frames.  First argument
	// is U01 random number, second argument is vector of parameters for distribution
	sampleNxtArrival func(float64, []float64) float64

	rngstrm *rngstream.RngStream //  each device has a common rng stream shared by all its strmQs
}

// createstrmQ is a constructor for the strmQueue
func createStrmQueue(strmRate, bndwdth float64, frameLen int, rngstrm *rngstream.RngStream) *strmQueue {
	sq := new(strmQueue)
	sq.inQ = make([]float64, 0)
	sq.active = false
	sq.strmRate = strmRate
	sq.bndwdth = bndwdth
	sq.frameLen = frameLen
	frameLenMbits := float64(frameLen) / 1e6
	sq.serviceTime = frameLenMbits / bndwdth
	sq.completes = 0.0
	sq.serving = true
	sq.rngstrm = rngstrm

	// make poisson arrivals the default
	sq.sampleNxtArrival = sampleExpRV
	return sq
}

// the flow rate into a strmQueue may change in the course of a simulation
func (sq *strmQueue) adjustStrmRate(strmRate float64) {
	if strmRate > 0.0 {
		sq.active = true
	} else {
		// strmQ goes inactive if the rate is zero
		sq.active = false
	}
	sq.strmRate = strmRate
}

// the interface bandwidth is set by network parameters, and
// modifications to it are carried to the strmQueue
func (sq *strmQueue) adjustIntrfcBndwdth(bndwdth float64) {
	sq.bndwdth = bndwdth
	frameLenMbits := float64(sq.frameLen) / 1e6
	sq.serviceTime = frameLenMbits / bndwdth
}

// the frame length can is set by network parameters, and
// modifications to it are carried to the strmQueue
func (sq *strmQueue) adjustStrmFrameLen(frameLen int) {
	sq.frameLen = frameLen
	frameLenMbits := float64(sq.frameLen) / 1e6
	sq.serviceTime = frameLenMbits / sq.bndwdth
}

// set the distribution of the inter-arrivals.  Pretty thin at present
func (sq *strmQueue) adjustInterArrivalDist(dist string) {
	switch dist {
	case "exponential", "exp", "expon":
		sq.sampleNxtArrival = sampleExpRV

	case "constant", "const":
		sq.sampleNxtArrival = sampleConst
	}
}

// qlen returns the number of strm arrivals enqueued for service
func (sq *strmQueue) qlen() int {
	return len(sq.inQ)
}

// popQ removes (and returns) the earliest item in the inQ
func (sq *strmQueue) popQ() (bool, float64) {
	var t float64
	if sq.active && len(sq.inQ) > 0 {
		t, sq.inQ = sq.inQ[0], sq.inQ[1:]
		return true, t
	}
	return false, 0.0
}

// appendQ adds an arrival to the end of the inQ
func (sq *strmQueue) appendQ(arrTime float64) {
	sq.inQ = append(sq.inQ, arrTime)
}

// earliestArrival gives the arrival time of the earliest arrival in the inQ, if present
func (sq *strmQueue) earliestArrival() (bool, float64) {
	if sq.active && sq.qlen() > 0 {
		return true, sq.inQ[0]
	}
	return false, 0.0
}

// srtServing tells the strmQueue that it is started now serving strm frames
func (sq *strmQueue) srtServing() {
	if !sq.active {
		return
	}

	sq.serving = true

	// if there is a strm frame to process we can set completes to when it
	// begins to service.  Notice that srtServing is only called after
	// the processing has been brought to the conclusion of a frame processing
	if sq.qlen() > 0 {
		sq.completes = roundFloat(sq.time+sq.serviceTime, rdigits)
	} else {
		sq.completes = 0.0
	}
}

// stopServing tells the strmQueue that it is not serving strm packets until told otherwise
func (sq *strmQueue) stopServing() {
	if !sq.active {
		return
	}
	sq.serving = false
	sq.completes = 0.0
}

// emptiesAt computes the time when all of the known work in the inQ
// has been served.   Note that it assumes that the inQ has been brought all the way forward
// already with an advance
func (sq *strmQueue) emptiesAfter(time float64) float64 {

	// nothing in the inQ right now means it is empty right now
	if !sq.active || sq.qlen() == 0 {
		return 0.0
	}
	return roundFloat(float64(len(sq.inQ))*sq.serviceTime, rdigits)
}

// precedes computes the service time spent starting now serving all
// the strm packets in the inQ that have arrival times earlier than argument arrTime
func (sq *strmQueue) precedes(arrTime float64) float64 {
	cnt := 0
	for _, t := range sq.inQ {
		if t < arrTime {
			cnt += 1
		} else {
			break
		}
	}
	// multiply the number of such arrivals by the service time
	return roundFloat(float64(cnt)*sq.serviceTime, rdigits)
}

var rdigits uint = 15

// round computed simulation time to avoid non-sensical comparisons
// induced by rounding error
func roundFloat(val float64, precision uint) float64 {
	ratio := math.Pow(10, float64(precision))
	return math.Round(val*ratio) / ratio
}

// expRV returns a sample of a exponentially distributed random number
func expRV(u01, rate float64) float64 {
	return -math.Log(1.0-u01) / rate
}

// sampleExpRV has the function signature expected by strmQ
// for calling a next interarrival time
func sampleExpRV(u01 float64, params []float64) float64 {
	return expRV(u01, params[0])
}

// sampleExpRV has the function signature expected by strmQ
// for calling a next interarrival time, here, a constant
func sampleConst(u01 float64, params []float64) float64 {
	return 1.0 / params[0]
}
