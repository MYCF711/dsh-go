// coldstart: 测目标 exe 的冷启动（spawn -> exit），10 次取中位数与 p95。
package main

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"time"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Println("usage: coldstart <exe> <n>")
		os.Exit(2)
	}
	exe := os.Args[1]
	n := 10
	fmt.Sscan(os.Args[2], &n)

	ds := make([]time.Duration, 0, n)
	for i := 0; i < n; i++ {
		st := time.Now()
		cmd := exec.Command(exe)
		cmd.Stdout = nil
		cmd.Stderr = nil
		if err := cmd.Run(); err != nil {
			fmt.Println("FAIL run:", err)
			os.Exit(1)
		}
		ds = append(ds, time.Since(st))
	}
	sorted := append([]time.Duration(nil), ds...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	median := sorted[len(sorted)/2]
	p95 := sorted[(len(sorted)*95+99)/100-1]
	var sum time.Duration
	for _, d := range ds {
		sum += d
	}
	fmt.Printf("n=%d median=%.2fms p95=%.2fms mean=%.2fms min=%.2fms max=%.2fms\n",
		n, ms(median), ms(p95), ms(sum/time.Duration(n)), ms(sorted[0]), ms(sorted[len(sorted)-1]))
	fmt.Print("samples_ms=")
	for _, d := range ds {
		fmt.Printf("%.2f ", ms(d))
	}
	fmt.Println()
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000.0 }
