// Copyright 2010 GoDCCP Authors. All rights reserved.
// Use of this source code is governed by a 
// license that can be found in the LICENSE file.

package ccid3

import (
	//"os"
	"math"
	//"github.com/petar/GoDCCP/dccp"
)

// rateCaclulator computers the allowed sending rate of the sender
type rateCalculator struct {
	x               uint32 // Current allowed sending rate, in bytes per second
	tld             int64  // Time Last Doubled (during slow start) or zero if unset; in ns since UTC zero
	recv_limit      uint32 // Receive limit, in bytes per second
	xRecvSet          // Data structure for x_recv_set (see RFC 5348)
}

const (
	X_MAX_INIT_WIN          = 4380           // Maximum size of initial window in bytes
	X_MAX_BACKOFF_INTERVAL  = 64e9           // Maximum backoff interval in ns (See RFC 5348, Section 4.3)
	X_RECV_MAX              = math.MaxInt32  // Maximum receive rate, in bytes per second
	X_RECV_SET_SIZE         = 3              // Size of x_recv_set
)

// Init resets the rate calculator for new use and returns the initial 
// allowed sending rate (in bytes per second). The latter is the rate
// to be used before the first feedback packet is received and hence before
// an RTT estimate is available.
func (t *rateCalculator) Init(now int64, ss uint32) {
	// The allowed sending rate before the first feedback packet is received
	// is one packet per second.
	t.x = ss
	// tld = 0 indicates that the first feedback packet has yet not been received.
	t.tld = 0
	// Because X_recv_set is initialized with a single item, with value Infinity, recv_limit is
	// set to Infinity for the first two round-trip times of the connection.  As a result, the
	// sending rate is not limited by the receive rate during that period.  This avoids the
	// problem of the sending rate being limited by the value of X_recv from the first feedback
	// packet.
	t.recv_limit = X_RECV_MAX
	t.xRecvSet.Init()
}

// onFirstRead is called internally to handle the very first feedback packet received.
func (t *rateCalculator) onFirstRead(now int64, ss uint32, rtt int64) uint32 {
	t.tld = now
	t.x = initRate(ss, rtt)
	return t.x
}

// initRate returns the allowed initial sending rate in bytes per second.
func initRate(ss uint32, rtt int64) uint32 {
	if ss <= 0 || rtt <= 0 {
		panic("unknown SS or RTT")
	}
	win := minu32(4*ss, maxu32(2*ss, X_MAX_INIT_WIN)) // window = bytes per round trip (bpr)
	return uint32(max64((1e9*int64(win)) / rtt, 1))
}

// Sender calls OnRead each time a new feedback packet arrives.
// OnRead returns the new allowed sending in bytes per second.
// x_recv is given in bytes per second.  
// lossRateInv equals zero if the loss rate is still unknown.
func (t *rateCalculator) OnRead(now int64, ss uint32, x_recv uint32, rtt int64, lossRateInv uint32, newLoss bool) uint32 {
	?? // The loss rate inv should never be < 1 after the first newLoss event
	?? // Fix for new UnknownLoss... value convention
	// lossSender: current rate, new loss reported, increase or decrease from prev event
	if t.tld <= 0 {
		return t.onFirstRead(now, ss, rtt)
	}
	if /* the entire interval covered by the feedback packet was a data-limited interval */ {
		if ?? /* the feedback packet reports a new loss event or an increase in the loss event rate p */ {
			t.xRecvSet.Halve()
			x_recv = (85 * x_recv) / 100
			t.xRecvSet.Maximize(now, x_recv)
			t.recv_limit = t.xRecvSet.Max()
		} else {
			t.xRecvSet.Maximize(now, x_recv)
			t.recv_limit = 2 * t.xRecvSet.Max()
		}
	} else {
		t.xRecvSet.Update(now, x_recv, rtt)
		t.recv_limit = 2 * t.xRecvSet.Max()
	}
	// Non-zero loss rate inverse indicates that we are in the post-slow start phase
	if lossRateInv > 0 {
		x_eq := t.thruEq(ss, rtt, lossRateInv)
		t.x = maxu32(
			minu32(x_eq, t.recv_limit), 
			(1e9*ss) / X_MAX_BACKOFF_INTERVAL
		)
	} else if now - t.tld >= rtt {
		// Initial slow-start
		t.x = max(min(2*t.x, recv_limit), initRate(ss, rtt))
		t.tld = now
	}
	// TODO: Place oscillation reduction code here (see RFC 5348, Section 4.3)
	return t.x
}

// thruEq returns the allowed sending rate, in bytes per second, according to the TCP
// throughput equation, for the regime b=1 and t_RTO=4*RTT (See RFC 5348, Section 3.1).
func (t *rateCalculator) thruEq(ss uint32, rtt int64, lossRateInv uint32) uint32 {
	bps := (1e3*1e9*int64(ss)) / (rtt * thruEqQ(lossRateInv))
	return uint32(bps)
}

// thruEqDenom computes the quantity 1e3*(sqrt(2*p/3) + 12*sqrt(3*p/8)*p*(1+32*p^2)).
func thruEqQ(lossRateInv uint32) int64 {
	j := min(int(lossRateInv), len(qTable))
	return qTable[j-1].Q
}

// —————
// xRecvSet maintains a set of recently received Receive Rates (via ReceiveRateOption)
type xRecvSet struct {
	set [X_RECV_SET_SIZE]xRecvEntry  // Set of recent rates
}

type xRecvEntry struct {
	Rate uint32   // Receive rate; in bytes per second
	Time int64    // Entry timestamp or zero if unset; in ns since UTC zero
}

// Init resets the xRecvSet object for new use
func (t *xRecvSet) Init() {
	for i, _ := range t.set {
		t.set[i] = xRecvEntry{}
	}
}

// Halve halves all the rates in the set
func (t *xRecvSet) Halve() {
	for i, _ := range t.set {
		t.set[i].Rate /= 2
	}
}

// Max returns the highest rate in the set; in bytes per second
func (t *xRecvSet) Max() uint32 {
	var set bool
	var r uint32
	for _, e := range t.set {
		if e.Time <= 0 {
			continue
		}
		if e.Rate > r {
			r = e.Rate
			set = true
		}
	}
	if !set {
		return X_RECV_MAX
	}
	return r
}

// The procedure for maximizing X_recv_set keeps a single value, the
// largest value from X_recv_set and the new X_recv.
//
//   Maximize X_recv_set():
//     Add X_recv to X_recv_set;
//     Delete initial value Infinity from X_recv_set, if it is still a member.
//     Set the timestamp of the largest item to the current time;
//     Delete all other items.
//
func (t *xRecvSet) Maximize(now int64, x_recv_bps uint32) {
	for i, e := range t.set {
		if e.Time > 0 {
			x_recv_bps = maxu32(x_recv_bps, e.Rate)
		}
		t.set[i] = xRecvEntry{}
	}
	t.set[0].Time = now
	t.set[0].Rate = x_recv_bps
}

// The procedure for updating X_recv_set keeps a set of X_recv values
// with timestamps from the two most recent round-trip times.
//
//   Update X_recv_set():
//     Add X_recv to X_recv_set;
//     Delete from X_recv_set values older than two round-trip times.
//
func (t *xRecvSet) Update(now int64, x_recv_bps uint32, rtt int64) {
	// Remove entries older than two RTT
	for i, e := range t.set {
		if e.Time > 0 && now - e.Time > 2*rtt {
			t.set[i] = xRecvEntry{}
		}
	}
	// Find free cell or oldest entry
	var j int = -1
	var j_time int64 = now
	for i, e := range t.set {
		if e.Time <= 0 {
			j = i
			break
		} 
		if e.Time <= j_time {
			j = i
			j_time = e.Time
		}
	}
	t.set[j].Rate = x_recv_bps
	t.set[j].Time = now
}
