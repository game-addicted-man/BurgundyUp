package main

import (
	"net/http"
	"sync/atomic"
	"time"
)

// 브라우저 모드에서는 창이 닫힌 것을 직접 알 수 없어서,
// 화면이 보내는 신호(ping)와 닫힐 때 보내는 신호(bye)로 판단합니다.

var byeAt atomic.Int64

func (a *App) apiBye(w http.ResponseWriter, r *http.Request) {
	byeAt.Store(time.Now().UnixNano())
	w.WriteHeader(204)
}

func (a *App) waitForPageClose(procDone <-chan struct{}) {
	select {
	case <-a.pinged:
	case <-time.After(3 * time.Minute):
		logf("화면이 연결되지 않아 종료합니다")
		return
	}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	var procGoneAt time.Time
	for range tick.C {
		last := time.Unix(0, a.lastPing.Load())
		_, _, active := a.queue.Snapshot()
		busy := active || a.dl.State().State == "running"

		// 창을 닫으면서 보낸 bye 뒤로 새 신호가 없으면(새로고침이 아니면) 종료
		if b := byeAt.Load(); b != 0 {
			bt := time.Unix(0, b)
			if last.Before(bt) && time.Since(bt) > 4*time.Second {
				logf("화면이 닫혔어요")
				return
			}
		}
		if procDone != nil && procGoneAt.IsZero() {
			select {
			case <-procDone:
				procGoneAt = time.Now()
			default:
			}
		}
		if !procGoneAt.IsZero() && last.Before(procGoneAt) && time.Since(procGoneAt) > 3*time.Second {
			logf("브라우저 창이 닫혔어요")
			return
		}
		idle := time.Since(last)
		if (idle > 2*time.Minute && !busy) || idle > 20*time.Minute {
			logf("화면 신호가 끊겨서 종료합니다")
			return
		}
	}
}
