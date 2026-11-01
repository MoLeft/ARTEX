package notify

import (
	"fmt"
	"time"
)

// QuietHours is a daily window in Beijing time. An empty window is disabled.
type QuietHours struct {
	Start     int
	End       int
	On        bool
	SendAfter bool
}

func ParseQuietHours(start, end string) (QuietHours, error) {
	if start == "" && end == "" {
		return QuietHours{}, nil
	}
	parse := func(value string) (int, error) {
		parsed, err := time.Parse("15:04", value)
		if err != nil || parsed.Format("15:04") != value {
			return 0, fmt.Errorf("免打扰时间须为 HH:MM")
		}
		return parsed.Hour()*60 + parsed.Minute(), nil
	}
	a, err := parse(start)
	if err != nil {
		return QuietHours{}, err
	}
	b, err := parse(end)
	if err != nil {
		return QuietHours{}, err
	}
	if a == b {
		return QuietHours{}, fmt.Errorf("免打扰开始和结束时间不能相同")
	}
	return QuietHours{Start: a, End: b, On: true}, nil
}

func (q QuietHours) Contains(at time.Time) bool {
	if !q.On {
		return false
	}
	beijing := at.UTC().Add(8 * time.Hour)
	minute := beijing.Hour()*60 + beijing.Minute()
	if q.Start < q.End {
		return minute >= q.Start && minute < q.End
	}
	return minute >= q.Start || minute < q.End
}
