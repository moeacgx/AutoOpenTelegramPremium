package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	cfg, err := LoadConfig()
	if err != nil {
		log.Fatal("配置加载失败: ", err)
	}

	app, err := NewApp(cfg)
	if err != nil {
		log.Fatal("初始化服务失败: ", err)
	}

	if cfg.ListenAddr != "" {
		if err := runServer(cfg, app); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal("HTTP 服务启动失败: ", err)
		}
		return
	}

	req, err := cfg.LegacyRequest()
	if err != nil {
		log.Fatal("单次执行参数无效: ", err)
	}

	resp, err := app.Fulfill(context.Background(), req)
	if err != nil {
		log.Fatal("发货失败: ", err)
	}

	log.Printf("发货成功 type=%s username=%s req_id=%s amount=%s hash=%s",
		resp.ProductType, resp.Username, resp.ReqID, resp.AmountTON, resp.TxHashBase64)
}

func runServer(cfg Config, app *App) error {
	server := &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: NewHTTPHandler(app, cfg),
	}

	go func() {
		log.Printf("HTTP 服务已启动，监听 %s", cfg.ListenAddr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("HTTP 服务异常退出: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	log.Println("收到退出信号，正在关闭 HTTP 服务...")
	return server.Shutdown(context.Background())
}
