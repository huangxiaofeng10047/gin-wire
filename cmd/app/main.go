package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/jassue/gin-wire/app/command"
	"github.com/jassue/gin-wire/config"
	"github.com/jassue/gin-wire/util/path"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

// 定义根路径变量，指向项目根目录
var (
	rootPath = path.RootPath()

	// 版本号，可在构建时指定
	Version string
	// 配置文件路径
	configPath string
	// 配置对象
	conf *config.Configuration
	// 日志写入器
	loggerWriter *lumberjack.Logger
	// 日志记录器
	logger *zap.Logger
)

// 初始化函数，设置命令行参数并在 Cobra 初始化时调用配置和日志初始化函数
func init() {
	// 注册 --conf 命令行参数，指定配置文件路径，默认值为项目根目录下的 conf/config.yaml
	pflag.StringVarP(&configPath, "conf", "", filepath.Join(rootPath, "conf", "config.yaml"), "config path, eg: --conf config.yaml")

	// 当 Cobra 初始化时，调用 initConfig 和 initLogger 函数
	cobra.OnInitialize(func() {
		initConfig()
		initLogger()
	})
}

// 主函数，程序入口
func main() {
	// 创建根命令对象
	rootCmd := &cobra.Command{
		// 命令使用说明
		Use: "app",
		// 命令执行函数
		Run: func(cmd *cobra.Command, args []string) {
			// 调用 wireApp 函数初始化应用
			app, cleanup, err := wireApp(conf, loggerWriter, logger)
			if err != nil {
				// 初始化失败，抛出异常
				panic(err)
			}
			// 程序结束时调用清理函数
			defer cleanup()

			// 启动应用并记录日志
			log.Printf("start app %s ...", Version)
			if err := app.Run(); err != nil {
				// 应用启动失败，抛出异常
				panic(err)
			}

			// 创建信号通道，监听 SIGINT 和 SIGTERM 信号
			quit := make(chan os.Signal)
			signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
			// 等待信号
			<-quit

			// 记录应用关闭日志
			log.Printf("shutdown app %s ...", Version)

			// 创建一个带 5 秒超时的上下文
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			// 程序结束时取消上下文
			defer cancel()

			// 关闭应用
			if err := app.Stop(ctx); err != nil {
				// 应用关闭失败，抛出异常
				panic(err)
			}
		},
	}

	// 注册命令到根命令
	command.Register(rootCmd, func() (*command.Command, func(), error) {
		// 调用 wireCommand 函数初始化命令
		return wireCommand(conf, loggerWriter, logger)
	})

	// 执行根命令
	if err := rootCmd.Execute(); err != nil {
		// 命令执行失败，抛出异常
		panic(err)
	}
}

// 初始化配置函数
func initConfig() {
	// 检查配置文件路径是否为绝对路径，如果不是则拼接为项目根目录下的路径
	if !filepath.IsAbs(configPath) {
		configPath = filepath.Join(rootPath, "conf", configPath)
	}
	// 打印根路径和配置文件路径
	fmt.Printf("rootPath: %s\n", rootPath)
	fmt.Printf("configPath: %s\n", configPath)
	fmt.Println("load config:" + configPath)

	// 创建一个新的 Viper 实例
	v := viper.New()
	// 设置配置文件路径
	v.SetConfigFile(configPath)
	// 设置配置文件类型为 YAML
	v.SetConfigType("yaml")
	if err := v.ReadInConfig(); err != nil {
		// 读取配置文件失败，抛出异常
		panic(fmt.Errorf("read config failed: %s \n", err))
	}

	// 将配置文件内容解析到 conf 对象
	if err := v.Unmarshal(&conf); err != nil {
		fmt.Println(err)
	}

	// 监听配置文件变化
	v.WatchConfig()
	v.OnConfigChange(func(in fsnotify.Event) {
		// 配置文件变化时打印日志
		fmt.Println("config file changed:", in.Name)
		defer func() {
			if err := recover(); err != nil {
				// 恢复配置文件解析时的异常并记录日志
				logger.Error("config file changed err:", zap.Any("err", err))
				fmt.Println(err)
			}
		}()
		if err := v.Unmarshal(&conf); err != nil {
			fmt.Println(err)
		}
	})
}

// 初始化日志函数
func initLogger() {
	var level zapcore.Level  // zap 日志等级
	var options []zap.Option // zap 配置项

	// 获取日志文件目录
	logFileDir := conf.Log.RootDir
	if !filepath.IsAbs(logFileDir) {
		// 如果不是绝对路径，则拼接为项目根目录下的路径
		logFileDir = filepath.Join(rootPath, logFileDir)
	}

	// 检查日志文件目录是否存在，如果不存在则创建
	if ok, _ := path.Exists(logFileDir); !ok {
		_ = os.Mkdir(conf.Log.RootDir, os.ModePerm)
	}

	// 根据配置文件中的日志等级设置 zap 日志等级
	switch conf.Log.Level {
	case "debug":
		level = zap.DebugLevel
		options = append(options, zap.AddStacktrace(level))
	case "info":
		level = zap.InfoLevel
	case "warn":
		level = zap.WarnLevel
	case "error":
		level = zap.ErrorLevel
		options = append(options, zap.AddStacktrace(level))
	case "dpanic":
		level = zap.DPanicLevel
	case "panic":
		level = zap.PanicLevel
	case "fatal":
		level = zap.FatalLevel
	default:
		level = zap.InfoLevel
	}

	// 调整编码器默认配置
	encoderConfig := zap.NewProductionEncoderConfig()
	// 设置时间格式
	encoderConfig.EncodeTime = func(time time.Time, encoder zapcore.PrimitiveArrayEncoder) {
		encoder.AppendString(time.Format("2006-01-02 15:04:05.000"))
	}
	// 设置日志等级格式
	encoderConfig.EncodeLevel = func(l zapcore.Level, encoder zapcore.PrimitiveArrayEncoder) {
		encoder.AppendString(conf.App.Env + "." + l.String())
	}

	// 创建日志写入器
	loggerWriter = &lumberjack.Logger{
		Filename:   filepath.Join(logFileDir, conf.Log.Filename),
		MaxSize:    conf.Log.MaxSize,
		MaxBackups: conf.Log.MaxBackups,
		MaxAge:     conf.Log.MaxAge,
		Compress:   conf.Log.Compress,
	}

	// 创建日志记录器
	logger = zap.New(zapcore.NewCore(zapcore.NewJSONEncoder(encoderConfig), zapcore.AddSync(loggerWriter), level), options...)
}
