package system

import (
	"bufio"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// NodeStats contains only aggregate host information safe to expose publicly.
// Values for memory and disk are MiB, matching the panel's node stats display.
type NodeStats struct {
	CPU struct {
		Model   string  `json:"model"`
		Threads int     `json:"threads"`
		FreqMHz float64 `json:"freq_mhz"`
		Used    float64 `json:"used"`
	} `json:"cpu"`
	Memory struct {
		Used      uint64 `json:"used"`
		Total     uint64 `json:"total"`
		SwapUsed  uint64 `json:"swap_used"`
		SwapTotal uint64 `json:"swap_total"`
	} `json:"memory"`
	Disk struct {
		Used  uint64 `json:"used"`
		Total uint64 `json:"total"`
	} `json:"disk"`
	System struct {
		OS     string  `json:"os"`
		Load1  float64 `json:"load_1"`
		Load5  float64 `json:"load_5"`
		Load15 float64 `json:"load_15"`
	} `json:"system"`
	Daemon struct {
		ServersRunning int `json:"servers_running"`
		ServersTotal   int `json:"servers_total"`
	} `json:"daemon"`
}

var nodeStatsCache struct {
	sync.Mutex
	updated time.Time
	stats   NodeStats
	busy    uint64
	total   uint64
}

// GetNodeStats samples host usage at most once every five seconds. The cache
// keeps the public endpoint inexpensive even if many visitors request it.
func GetNodeStats(dataPath string) (NodeStats, error) {
	nodeStatsCache.Lock()
	defer nodeStatsCache.Unlock()
	if time.Since(nodeStatsCache.updated) < 5*time.Second {
		return nodeStatsCache.stats, nil
	}

	var stats NodeStats
	stats.CPU.Threads = runtime.NumCPU()
	stats.System.OS = runtime.GOOS
	if err := readCPUInfo(&stats); err != nil {
		return NodeStats{}, err
	}
	busy, total, err := readCPUTicks()
	if err != nil {
		return NodeStats{}, err
	}
	if total > nodeStatsCache.total && busy >= nodeStatsCache.busy {
		stats.CPU.Used = 100 * float64(busy-nodeStatsCache.busy) / float64(total-nodeStatsCache.total)
	}
	nodeStatsCache.busy, nodeStatsCache.total = busy, total
	if err := readMemory(&stats); err != nil {
		return NodeStats{}, err
	}
	if err := readDisk(&stats, dataPath); err != nil {
		return NodeStats{}, err
	}
	if err := readLoad(&stats); err != nil {
		return NodeStats{}, err
	}
	nodeStatsCache.stats = stats
	nodeStatsCache.updated = time.Now()
	return stats, nil
}

func readCPUInfo(stats *NodeStats) error {
	f, err := os.Open("/proc/cpuinfo")
	if err != nil {
		return err
	}
	defer f.Close()
	for scanner := bufio.NewScanner(f); scanner.Scan(); {
		line := scanner.Text()
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "model name":
			if stats.CPU.Model == "" {
				stats.CPU.Model = strings.TrimSpace(value)
			}
		case "cpu MHz":
			if stats.CPU.FreqMHz == 0 {
				stats.CPU.FreqMHz, _ = strconv.ParseFloat(strings.TrimSpace(value), 64)
			}
		}
	}
	return nil
}

func readCPUTicks() (uint64, uint64, error) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		return 0, 0, fmt.Errorf("cannot read CPU counters")
	}
	fields := strings.Fields(scanner.Text())
	if len(fields) < 8 || fields[0] != "cpu" {
		return 0, 0, fmt.Errorf("invalid CPU counters")
	}
	var ticks [7]uint64
	for i := range ticks {
		ticks[i], err = strconv.ParseUint(fields[i+1], 10, 64)
		if err != nil {
			return 0, 0, err
		}
	}
	busy := ticks[0] + ticks[1] + ticks[2] + ticks[5] + ticks[6]
	total := busy + ticks[3] + ticks[4]
	return busy, total, nil
}

func readMemory(stats *NodeStats) error {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return err
	}
	defer f.Close()
	values := map[string]uint64{}
	for scanner := bufio.NewScanner(f); scanner.Scan(); {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		value, _ := strconv.ParseUint(fields[1], 10, 64)
		values[strings.TrimSuffix(fields[0], ":")] = value / 1024
	}
	stats.Memory.Total = values["MemTotal"]
	stats.Memory.Used = stats.Memory.Total - values["MemAvailable"]
	stats.Memory.SwapTotal = values["SwapTotal"]
	stats.Memory.SwapUsed = stats.Memory.SwapTotal - values["SwapFree"]
	return nil
}

func readDisk(stats *NodeStats, dataPath string) error {
	var fs unix.Statfs_t
	if err := unix.Statfs(dataPath, &fs); err != nil {
		return err
	}
	stats.Disk.Total = fs.Blocks * uint64(fs.Bsize) / 1048576
	stats.Disk.Used = (fs.Blocks - fs.Bavail) * uint64(fs.Bsize) / 1048576
	return nil
}

func readLoad(stats *NodeStats) error {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return err
	}
	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return fmt.Errorf("invalid load averages")
	}
	stats.System.Load1, _ = strconv.ParseFloat(fields[0], 64)
	stats.System.Load5, _ = strconv.ParseFloat(fields[1], 64)
	stats.System.Load15, _ = strconv.ParseFloat(fields[2], 64)
	return nil
}
