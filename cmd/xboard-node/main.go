package main

import (
	"context"
	"flag"
	"fmt"
	"math"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/cedar2025/xboard-node/internal/config"
	"github.com/cedar2025/xboard-node/internal/machine"
	"github.com/cedar2025/xboard-node/internal/nlog"
	"github.com/cedar2025/xboard-node/internal/service"
)

var (
	version   = "dev"
	buildTime = "unknown"
)

func main() {
	configPath := flag.String("c", "config.yml", "config file path")
	showVersion := flag.Bool("v", false, "show version")
	flag.Parse()

	if *showVersion {
		fmt.Printf("xboard-node %s (built %s)\n", version, buildTime)
		os.Exit(0)
	}

	rootCfg, err := config.LoadRoot(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}

	instances, err := rootCfg.NormalizeInstances()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to normalize config: %v\n", err)
		os.Exit(1)
	}
	config.InitLogger(instances[0].Log)

	// Apply runtime memory tuning before anything else allocates.
	applyRuntimeConfig(instances[0].Runtime)

	runWithReload(rootCfg, *configPath)
}

// runWithReload restarts all node services when the config file changes.
func runWithReload(initialRoot *config.RootConfig, configPath string) {
	var healthSrv *http.Server
	var healthPort int
	startHealth := func(port int) error {
		if port <= 0 {
			healthPort = 0
			return nil
		}
		ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
		if err != nil {
			return fmt.Errorf("listen health port %d: %w", port, err)
		}
		mux := http.NewServeMux()
		// /healthz 仅表示进程存活，不代表节点已就绪。
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"status":"ok"}`))
		})
		srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
		healthSrv = srv
		healthPort = port
		go func(srv *http.Server, p int) {
			nlog.Core().Debug(fmt.Sprintf("health check listening on :%d", p))
			if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
				nlog.Core().Warn("health check server stopped", "error", err)
			}
		}(srv, port)
		return nil
	}

	initialInstances, err := initialRoot.NormalizeInstances()
	if err != nil {
		nlog.Core().Error("failed to normalize initial config", "error", err)
		os.Exit(1)
	}
	if err := config.ValidateStartupLayout(initialInstances); err != nil {
		nlog.Core().Error("startup layout validation failed", "error", err)
		os.Exit(1)
	}
	if err := startHealth(initialInstances[0].HealthPort); err != nil {
		nlog.Core().Error("failed to start health check listener", "error", err)
		os.Exit(1)
	}
	defer func() {
		if healthSrv != nil {
			healthSrv.Close()
		}
	}()

	// 信号监听属于进程生命周期，不能随单次重载一起结束。
	processCtx, stopProcess := context.WithCancel(context.Background())
	defer stopProcess()
	signalCtx, stopSignals := context.WithCancel(context.Background())
	defer stopSignals()
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	go handleShutdownSignals(signalCtx, stopProcess, sigCh, 15*time.Second, os.Exit)

	for root := initialRoot; ; {
		ctx, cancel := context.WithCancel(processCtx)

		reloadCh := make(chan *config.RootConfig, 1)

		watcher, err := config.WatchConfigRoot(ctx, configPath, func(newCfg *config.RootConfig) {
			select {
			case reloadCh <- newCfg:
			default:
			}
		})
		if err != nil {
			nlog.Core().Warn("config watcher unavailable, hot-reload disabled", "error", err)
		}

		instances, err := root.NormalizeInstances()
		if err != nil {
			nlog.Core().Error("failed to normalize config", "error", err)
			os.Exit(1)
		}
		if err := config.ValidateStartupLayout(instances); err != nil {
			nlog.Core().Error("startup layout validation failed", "error", err)
			os.Exit(1)
		}

		if instances[0].HealthPort != healthPort {
			if healthSrv != nil {
				healthSrv.Close()
				healthSrv = nil
				healthPort = 0
			}
			if err := startHealth(instances[0].HealthPort); err != nil {
				nlog.Core().Error("failed to restart health check listener on reload, continuing without health port", "error", err)
			}
		}

		errCh := make(chan error, 1)
		sendErr := func(err error) {
			select {
			case errCh <- err:
			default:
			}
		}
		doneCh := make(chan struct{})
		var wg sync.WaitGroup
		for _, instanceCfg := range instances {
			instanceCfg := instanceCfg
			wg.Add(1)
			go func() {
				defer wg.Done()
				if instanceCfg.IsMachineMode() {
					nlog.Core().Info("starting machine instance", "instance", instanceCfg.InstanceID, "machine_id", instanceCfg.Machine.MachineID, "panel_url", instanceCfg.Panel.URL)
					orch := machine.New(instanceCfg)
					if err := orch.Run(ctx); err != nil {
						nlog.Core().Error("machine instance exited with error", "instance", instanceCfg.InstanceID, "error", err)
						sendErr(err)
						cancel()
					}
					return
				}
				nodes := instanceCfg.ExpandNodes()
				nlog.Core().Info("starting node instance", "instance", instanceCfg.InstanceID, "nodes", len(nodes), "panel_url", instanceCfg.Panel.URL)
				var instanceWG sync.WaitGroup
				for idx, nodeCfg := range nodes {
					nodeCfg := nodeCfg
					instanceWG.Add(1)
					go func(idx int) {
						defer instanceWG.Done()
						if idx > 0 {
							delay := time.Duration(idx) * 250 * time.Millisecond
							if delay > 2*time.Second {
								delay = 2 * time.Second
							}
							select {
							case <-time.After(delay):
							case <-ctx.Done():
								return
							}
						}
						svc := service.New(nodeCfg)
						if err := svc.Run(ctx); err != nil {
							nlog.Core().Error("node service exited with error", "instance", nodeCfg.InstanceID, "node_id", nodeCfg.Panel.NodeID, "error", err)
							sendErr(err)
							cancel()
						}
					}(idx)
				}
				instanceWG.Wait()
			}()
		}
		go func() { wg.Wait(); close(doneCh) }()

		var newRoot *config.RootConfig
		var newInstances []*config.Config
	waitLoop:
		for {
			select {
			case candidate := <-reloadCh:
				norm, normErr := candidate.NormalizeInstances()
				if normErr != nil {
					nlog.Core().Error("reloaded config invalid (normalize), keeping current config", "error", normErr)
					continue
				}
				if layoutErr := config.ValidateStartupLayout(norm); layoutErr != nil {
					nlog.Core().Error("reloaded config invalid (layout), keeping current config", "error", layoutErr)
					continue
				}
				newRoot = candidate
				newInstances = norm
				nlog.Core().Info("config changed, restarting all services...")
				cancel()
				<-doneCh
				break waitLoop
			case <-doneCh:
				break waitLoop
			}
		}

		cancel()
		if watcher != nil {
			watcher.Stop()
		}

		if newRoot == nil || processCtx.Err() != nil {
			close(errCh)
			if err := <-errCh; err != nil {
				os.Exit(1)
			}
			nlog.Core().Info("stopped")
			return
		}

		config.InitLogger(newInstances[0].Log)
		applyRuntimeConfig(newInstances[0].Runtime)
		root = newRoot
		nlog.Core().Info("reload complete, services restarting with new config")
	}
}

// handleShutdownSignals 在进程退出前持续处理信号和强制退出超时。
func handleShutdownSignals(ctx context.Context, cancel context.CancelFunc, signals <-chan os.Signal, timeout time.Duration, exit func(int)) {
	var sig os.Signal
	select {
	case sig = <-signals:
	case <-ctx.Done():
		return
	}

	// 先启动计时，再异步记录日志和取消，避免任何阻塞使保护失效。
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	go func() {
		cancel()
		nlog.Core().Info(fmt.Sprintf("received %v, shutting down...", sig))
	}()
	select {
	case <-signals:
		exit(1)
	case <-timer.C:
		exit(2)
	case <-ctx.Done():
	}
}

// applyRuntimeConfig wires up Go runtime memory limits from the config file.
// Both settings can also be overridden by environment variables (GOMEMLIMIT /
// GOGC) — the env vars take precedence because Go's runtime reads them before
// we can call these functions, but we set them here for completeness and so
// the values are logged.
func applyRuntimeConfig(rt config.RuntimeConfig) {
	// GOGC
	if rt.GoGCPercent > 0 {
		prev := debug.SetGCPercent(rt.GoGCPercent)
		nlog.Core().Info("runtime: GOGC set", "gogc", rt.GoGCPercent, "prev", prev)
	}

	// GOMEMLIMIT — parse human-readable size string (e.g. "30MiB")
	if rt.GoMemLimit != "" {
		limit, err := parseMemLimit(rt.GoMemLimit)
		if err != nil {
			nlog.Core().Warn("runtime: invalid gomemlimit, ignoring", "value", rt.GoMemLimit, "error", err)
		} else {
			prev := debug.SetMemoryLimit(limit)
			nlog.Core().Info("runtime: GOMEMLIMIT set",
				"limit", rt.GoMemLimit,
				"bytes", limit,
				"prev_bytes", prev,
			)
		}
	}
}

// parseMemLimit converts a human-readable positive size string to bytes.
// Supported suffixes (case-insensitive):
//   - IEC binary: KiB, MiB, GiB, TiB
//   - SI decimal: KB, MB, GB, TB, K, M, G, T
//   - Bytes: B or bare integer
func parseMemLimit(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty size")
	}
	suffixes := []struct {
		suffix string
		mult   int64
	}{
		{"TIB", 1 << 40},
		{"GIB", 1 << 30},
		{"MIB", 1 << 20},
		{"KIB", 1 << 10},
		{"TB", 1_000_000_000_000},
		{"GB", 1_000_000_000},
		{"MB", 1_000_000},
		{"KB", 1_000},
		{"T", 1 << 40},
		{"G", 1 << 30},
		{"M", 1 << 20},
		{"K", 1 << 10},
		{"B", 1},
	}
	upper := strings.ToUpper(s)
	numStr := upper
	var mult int64 = 1
	for _, sf := range suffixes {
		if strings.HasSuffix(upper, sf.suffix) {
			numStr = strings.TrimSpace(strings.TrimSuffix(upper, sf.suffix))
			mult = sf.mult
			break
		}
	}
	n, err := strconv.ParseInt(numStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size format %q: %w", s, err)
	}
	if n <= 0 {
		return 0, fmt.Errorf("memory limit must be positive, got %q", s)
	}
	if n > math.MaxInt64/mult {
		return 0, fmt.Errorf("memory limit overflows int64: %q", s)
	}
	return n * mult, nil
}
