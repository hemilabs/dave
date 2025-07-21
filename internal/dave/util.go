// Copyright (c) 2025 Hemi Labs, Inc.
// Use of this source code is governed by the MIT License,
// which can be found in the LICENSE file.

package dave

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type ProgressBar struct {
	total     int
	current   int
	startTime time.Time

	mtx sync.RWMutex
}

func NewProgressBar(ctx context.Context, total int) *ProgressBar {
	p := ProgressBar{
		total:     total,
		startTime: time.Now(),
	}
	fmt.Println("")

	go func() {
		for {
			select {
			case <-ctx.Done():
				p.play()
				fmt.Println()
				return
			case <-time.After(1000 * time.Millisecond):
				finished := p.play()
				if finished {
					fmt.Println()
					return
				}
			}
		}
	}()

	return &p
}

func (p *ProgressBar) Update(add int) {
	p.mtx.Lock()
	defer p.mtx.Unlock()

	p.current += add
}

func (p *ProgressBar) play() bool {
	p.mtx.RLock()
	defer p.mtx.RUnlock()

	percent := int((float32(p.current) / float32(p.total)) * 100)
	var bar string
	for range percent / 2 {
		bar += "#"
	}
	elapsed := time.Since(p.startTime)

	fmt.Printf("\r[%-50s]%3d%% %8d/%d %s", bar, percent, p.current, p.total, elapsed)

	return p.current >= p.total
}

func CalculateDirSize(path string) (int, error) {
	var total int64
	err := filepath.Walk(path, func(_ string, i os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !i.IsDir() {
			total += i.Size()
		}
		return err
	})
	return int(total), err
}
