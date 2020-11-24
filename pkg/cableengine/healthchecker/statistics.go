package healthchecker

import (
	"math"
)

type Statistics struct {
	previousRtts []uint64
	sum          uint64
	mean         uint64
	stdDev       uint64
	lastRtt      uint64
	minRtt       uint64
	maxRtt       uint64
	sqrDiff      uint64
	index        uint64
	size         uint64
}

func (s *Statistics) updateStatistics(rtt uint64) {
	s.lastRtt = rtt

	// TODO Take more samples while resetting, for example samples in last 2 hours
	if s.index == s.size {
		// Resetting since the incremental SD calculated have an error factor due to truncation which
		// could be significant as count increases.
		s.index = 2
		s.previousRtts[0] = s.previousRtts[s.size-2]
		s.previousRtts[1] = s.previousRtts[s.size-1]
		s.sum = s.previousRtts[0] + s.previousRtts[1]
		s.mean = s.sum / 2
		s.sqrDiff = uint64((int64(s.previousRtts[0]-s.mean))*(int64(s.previousRtts[0]-s.mean)) +
			(int64(s.previousRtts[1]-s.mean))*(int64(s.previousRtts[1]-s.mean)))
	}

	if (s.index + 1) > 1 {
		s.previousRtts[s.index] = rtt
		if s.minRtt == 0 || s.minRtt > rtt {
			s.minRtt = rtt
		}

		if s.maxRtt < rtt {
			s.maxRtt = rtt
		}

		s.sum += rtt
		oldMean := s.mean
		s.mean = s.sum / (s.index + 1)
		s.sqrDiff += uint64(((int64(rtt - oldMean)) * int64((rtt - s.mean))))
		s.stdDev = uint64(math.Sqrt(float64(s.sqrDiff / (s.index + 1))))
	} else {
		s.sum = rtt
		s.sqrDiff = 0
		s.minRtt = rtt
		s.maxRtt = rtt
		s.mean = rtt
		s.sum = rtt
		s.previousRtts[s.index] = rtt
		s.stdDev = 0
	}

	s.index++
}
