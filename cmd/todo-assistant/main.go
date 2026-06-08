package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"todo-assistant/internal/agent"
	"todo-assistant/internal/config"
	"todo-assistant/internal/memo"
	"todo-assistant/internal/scheduler"
	"todo-assistant/internal/todo"
	"todo-assistant/internal/tui"
	"todo-assistant/internal/wecom"
)

const appName = "todo-assistant"

func main() {
	if err := run(os.Args); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) < 2 {
		printUsage()
		return nil
	}

	switch args[1] {
	case "tui":
		return runTUI(args[2:])
	case "serve":
		return runServe(args[2:])
	case "config":
		return runConfig(args[2:])
	case "doctor":
		return runDoctor(args[2:])
	default:
		printUsage()
		return fmt.Errorf("unknown command %q", args[1])
	}
}

func printUsage() {
	fmt.Printf(`%s

Usage:
  todo-assistant tui      Start the terminal UI
  todo-assistant serve    Start WeCom long connection and reminders
  todo-assistant config   Configure model and WeCom settings
  todo-assistant doctor   Check config, storage, and model connectivity

`, appName)
}

func commonFlags(args []string) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet(appName, flag.ContinueOnError)
	configPath := fs.String("config", filepath.Join(config.DefaultDataDir(), "config.json"), "config file path")
	return fs, configPath
}

func openStore(cfg config.Config) (*todo.Store, error) {
	dataDir := cfg.Local.DataDir
	if dataDir == "" {
		dataDir = config.DefaultDataDir()
	}
	return todo.NewStore(filepath.Join(dataDir, "todos.json"))
}

func openMemoStore(cfg config.Config) (*memo.Store, error) {
	dataDir := cfg.Local.DataDir
	if dataDir == "" {
		dataDir = config.DefaultDataDir()
	}
	return memo.NewStore(filepath.Join(dataDir, "memos.json"))
}

func setupLogger(cfg config.Config) (*os.File, error) {
	dataDir := cfg.Local.DataDir
	if dataDir == "" {
		dataDir = config.DefaultDataDir()
	}
	logDir := filepath.Join(dataDir, "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil, err
	}
	logPath := filepath.Join(logDir, "todo-assistant.log")
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	log.SetOutput(io.MultiWriter(os.Stdout, f))
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Printf("logging to %s", logPath)
	return f, nil
}

func runTUI(args []string) error {
	fs, configPath := commonFlags(args)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.LoadOrDefault(*configPath)
	if err != nil {
		return err
	}
	store, err := openStore(cfg)
	if err != nil {
		return err
	}
	svc := todo.NewService(store, time.Local)
	return tui.Run(svc)
}

func runServe(args []string) error {
	fs, configPath := commonFlags(args)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.LoadOrDefault(*configPath)
	if err != nil {
		return err
	}
	logFile, err := setupLogger(cfg)
	if err != nil {
		return err
	}
	defer logFile.Close()
	loc, err := cfg.Location()
	if err != nil {
		return err
	}
	store, err := openStore(cfg)
	if err != nil {
		return err
	}
	svc := todo.NewService(store, loc)
	memoStore, err := openMemoStore(cfg)
	if err != nil {
		return err
	}
	memoSvc := memo.NewService(memoStore, loc)
	model := agent.NewOpenAIClient(cfg.Model)
	assistant, err := agent.NewWithSQLiteAndMemo(model, svc, memoSvc, loc, cfg.Local.DataDir)
	if err != nil {
		return fmt.Errorf("open sqlite agent runtime: %w", err)
	}
	defer assistant.Close()
	wc := wecom.NewClient(cfg.WeCom, log.Default())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	reminders := scheduler.New(svc, wc, scheduler.Options{
		HomeChatID: cfg.WeCom.HomeChatID,
		Interval:   time.Minute,
		Now:        time.Now,
	})

	wc.OnText(func(ctx context.Context, msg wecom.InboundText) {
		log.Printf("wecom inbound: req_id=%s msg_id=%s chat_id=%s user_id=%s text=%q", msg.Frame.Headers.ReqID, msg.MsgID, msg.ChatID, msg.UserID, msg.Text)
		ctx = todo.WithChatID(ctx, msg.ChatID)
		ctx = memo.WithChatID(ctx, msg.ChatID)
		if cfg.WeCom.HomeChatID == "" && msg.ChatID != "" {
			cfg.WeCom.HomeChatID = msg.ChatID
			_ = config.Save(*configPath, cfg)
			reminders.SetHomeChatID(msg.ChatID)
		}
		streamID := "todo_" + msg.MsgID
		if streamID == "todo_" {
			streamID = "todo_" + strings.ReplaceAll(msg.Frame.Headers.ReqID, "-", "_")
		}
		var lastStreamed string
		var lastStreamAt time.Time
		streamDeltas := 0
		reply, err := assistant.HandleTextStream(ctx, msg.Text, func(partial string) {
			partial = strings.TrimSpace(partial)
			if partial == "" || partial == lastStreamed {
				return
			}
			if streamDeltas >= 2 || utf8.RuneCountInString(partial) < 40 {
				return
			}
			if !lastStreamAt.IsZero() && time.Since(lastStreamAt) < time.Second {
				return
			}
			lastStreamed = partial
			lastStreamAt = time.Now()
			streamDeltas++
			if err := wc.ReplyStream(ctx, msg, streamID, partial, false); err != nil {
				log.Printf("stream delta failed: %v", err)
			}
		})
		if err != nil {
			reply = "处理失败：" + err.Error()
		}
		log.Printf("wecom outbound: req_id=%s stream_id=%s text=%q", msg.Frame.Headers.ReqID, streamID, reply)
		if strings.TrimSpace(reply) == "" {
			reply = "我处理完了。"
		}
		if err := wc.ReplyStream(ctx, msg, streamID, reply, true); err != nil {
			log.Printf("reply failed: %v", err)
		}
	})

	go func() {
		if err := reminders.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("scheduler stopped: %v", err)
		}
	}()

	log.Println("starting WeCom AI Bot long connection")
	return wc.Run(ctx)
}

func runConfig(args []string) error {
	fs, configPath := commonFlags(args)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.LoadOrDefault(*configPath)
	if err != nil {
		return err
	}
	reader := os.Stdin
	read := func(label, current string) string {
		if current != "" {
			fmt.Printf("%s [%s]: ", label, current)
		} else {
			fmt.Printf("%s: ", label)
		}
		var line string
		fmt.Fscanln(reader, &line)
		line = strings.TrimSpace(line)
		if line == "" {
			return current
		}
		return line
	}

	cfg.Model.BaseURL = read("OpenAI-compatible base URL", cfg.Model.BaseURL)
	cfg.Model.APIKey = read("API key", cfg.Model.APIKey)
	cfg.Model.Model = read("Model", cfg.Model.Model)
	cfg.WeCom.BotID = read("WeCom Bot ID", cfg.WeCom.BotID)
	cfg.WeCom.Secret = read("WeCom Secret", cfg.WeCom.Secret)
	cfg.WeCom.WebSocketURL = read("WeCom WebSocket URL", cfg.WeCom.WebSocketURL)
	cfg.Local.DataDir = read("Data dir", cfg.Local.DataDir)
	cfg.Local.Timezone = read("Timezone", cfg.Local.Timezone)

	return config.Save(*configPath, cfg)
}

func runDoctor(args []string) error {
	fs, configPath := commonFlags(args)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.LoadOrDefault(*configPath)
	if err != nil {
		return err
	}
	fmt.Println("config:", *configPath)
	if _, err := cfg.Location(); err != nil {
		fmt.Println("timezone: FAIL", err)
	} else {
		fmt.Println("timezone: OK")
	}
	store, err := openStore(cfg)
	if err != nil {
		return err
	}
	if _, err := store.List(); err != nil {
		fmt.Println("storage: FAIL", err)
	} else {
		fmt.Println("storage: OK")
	}
	memoStore, err := openMemoStore(cfg)
	if err != nil {
		return err
	}
	if _, err := memoStore.List(); err != nil {
		fmt.Println("memo storage: FAIL", err)
	} else {
		fmt.Println("memo storage: OK")
	}
	if cfg.Model.BaseURL == "" || cfg.Model.APIKey == "" || cfg.Model.Model == "" {
		fmt.Println("model: SKIP missing base_url/api_key/model")
	} else if err := agent.NewOpenAIClient(cfg.Model).Ping(context.Background()); err != nil {
		fmt.Println("model: FAIL", err)
	} else {
		fmt.Println("model: OK")
	}
	if cfg.WeCom.BotID == "" || cfg.WeCom.Secret == "" {
		fmt.Println("wecom: SKIP missing bot_id/secret")
	} else {
		fmt.Println("wecom: OK config present")
	}
	return nil
}
