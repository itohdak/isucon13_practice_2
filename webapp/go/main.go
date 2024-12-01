package main

// ISUCON的な参考: https://github.com/isucon/isucon12-qualify/blob/main/webapp/go/isuports.go#L336
// sqlx的な参考: https://jmoiron.github.io/sqlx/

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"

	"github.com/gorilla/sessions"
	"github.com/labstack/echo-contrib/session"
	echolog "github.com/labstack/gommon/log"

	"github.com/kaz/pprotein/integration/standalone"
)

const (
	listenPort                     = 8080
	powerDNSSubdomainAddressEnvKey = "ISUCON13_POWERDNS_SUBDOMAIN_ADDRESS"
)

var (
	powerDNSSubdomainAddress string
	dbConn                   *sqlx.DB
	secret                   = []byte("isucon13_session_cookiestore_defaultsecret")

	userPrevActivity sync.Map
	userActivity     sync.Map
)

// パターンごとにマッチした部分をそのまま置換する関数
func replaceWithMatchedPattern(input string, patterns []string) (string, error) {
	// 結果を保持する文字列
	result := input

	for _, pattern := range patterns {
		// パターンをコンパイル
		re, err := regexp.Compile(pattern)
		if err != nil {
			return "", fmt.Errorf("正規表現のコンパイルに失敗しました: %v", err)
		}

		// マッチした部分をそのまま置換
		result = re.ReplaceAllString(result, pattern)
	}

	return result, nil
}

func myMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		rawURL := c.Request().URL.Path
		currURL, _ := replaceWithMatchedPattern(rawURL, []string{
			`^/api/initialize$`,
			`^/api/tag$`,
			`^/api/user/[0-9a-zA-Z]+/theme$`,
			`^/api/livestream/reservation$`,
			`^/api/livestream/search$`,
			`^/api/livestream$`,
			`^/api/user/[0-9a-zA-Z]+/livestream$`,
			`^/api/livestream/[0-9a-zA-Z]+$`,
			`^/api/livestream/[0-9a-zA-Z]+/livecomment$`,
			`^/api/livestream/[0-9a-zA-Z]+/livecomment$`,
			`^/api/livestream/[0-9a-zA-Z]+/reaction$`,
			`^/api/livestream/[0-9a-zA-Z]+/reaction$`,
			`^/api/livestream/[0-9a-zA-Z]+/report$`,
			`^/api/livestream/[0-9a-zA-Z]+/ngwords$`,
			`^/api/livestream/[0-9a-zA-Z]+/livecomment/[0-9a-zA-Z]+/report$`,
			`^/api/livestream/[0-9a-zA-Z]+/moderate$`,
			`^/api/livestream/[0-9a-zA-Z]+/enter$`,
			`^/api/livestream/[0-9a-zA-Z]+/exit$`,
			`^/api/register$`,
			`^/api/login$`,
			`^/api/user/me$`,
			`^/api/user/[0-9a-zA-Z]+$`,
			`^/api/user/[0-9a-zA-Z]+/statistics$`,
			`^/api/user/[0-9a-zA-Z]+/icon$`,
			`^/api/icon$`,
			`^/api/livestream/[0-9a-zA-Z]+/statistics$`,
			`^/api/payment$`,
		})
		cookie, err := c.Cookie("my_session_id")
		sid := cookie.Value
		if err != nil {
			uuid, _ := uuid.NewRandom()
			c.SetCookie(&http.Cookie{
				Name:  "my_session_id",
				Value: fmt.Sprintf("%s", uuid),
			})
			sid = fmt.Sprintf("%s", uuid)
		} else {
			if prevURL, ok := userPrevActivity.Load(sid); ok {
				trans := fmt.Sprintf("%s --> %s", prevURL, currURL)
				cnt, _ := userActivity.LoadOrStore(trans, int64(0))
				userActivity.Store(trans, cnt.(int64)+1)
			}
		}
		userPrevActivity.Store(sid, currURL)
		if err = next(c); err != nil {
			c.Error(err)
		}
		return nil
	}
}

func init() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)
	if secretKey, ok := os.LookupEnv("ISUCON13_SESSION_SECRETKEY"); ok {
		secret = []byte(secretKey)
	}
}

type InitializeResponse struct {
	Language string `json:"language"`
}

func connectDB(logger echo.Logger) (*sqlx.DB, error) {
	const (
		networkTypeEnvKey = "ISUCON13_MYSQL_DIALCONFIG_NET"
		addrEnvKey        = "ISUCON13_MYSQL_DIALCONFIG_ADDRESS"
		portEnvKey        = "ISUCON13_MYSQL_DIALCONFIG_PORT"
		userEnvKey        = "ISUCON13_MYSQL_DIALCONFIG_USER"
		passwordEnvKey    = "ISUCON13_MYSQL_DIALCONFIG_PASSWORD"
		dbNameEnvKey      = "ISUCON13_MYSQL_DIALCONFIG_DATABASE"
		parseTimeEnvKey   = "ISUCON13_MYSQL_DIALCONFIG_PARSETIME"
	)

	conf := mysql.NewConfig()

	// 環境変数がセットされていなかった場合でも一旦動かせるように、デフォルト値を入れておく
	// この挙動を変更して、エラーを出すようにしてもいいかもしれない
	conf.Net = "tcp"
	conf.Addr = net.JoinHostPort("127.0.0.1", "3306")
	conf.User = "isucon"
	conf.Passwd = "isucon"
	conf.DBName = "isupipe"
	conf.ParseTime = true

	if v, ok := os.LookupEnv(networkTypeEnvKey); ok {
		conf.Net = v
	}
	if addr, ok := os.LookupEnv(addrEnvKey); ok {
		if port, ok2 := os.LookupEnv(portEnvKey); ok2 {
			conf.Addr = net.JoinHostPort(addr, port)
		} else {
			conf.Addr = net.JoinHostPort(addr, "3306")
		}
	}
	if v, ok := os.LookupEnv(userEnvKey); ok {
		conf.User = v
	}
	if v, ok := os.LookupEnv(passwordEnvKey); ok {
		conf.Passwd = v
	}
	if v, ok := os.LookupEnv(dbNameEnvKey); ok {
		conf.DBName = v
	}
	if v, ok := os.LookupEnv(parseTimeEnvKey); ok {
		parseTime, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("failed to parse environment variable '%s' as bool: %+v", parseTimeEnvKey, err)
		}
		conf.ParseTime = parseTime
	}

	db, err := sqlx.Open("mysql", conf.FormatDSN())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(10)

	if err := db.Ping(); err != nil {
		return nil, err
	}

	return db, nil
}

func initializeHandler(c echo.Context) error {
	if out, err := exec.Command("../sql/init.sh").CombinedOutput(); err != nil {
		c.Logger().Warnf("init.sh failed with err=%s", string(out))
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to initialize: "+err.Error())
	}

	go func() {
		if _, err := http.Get("http://pprotein.maca.jp:9000/api/group/collect"); err != nil {
			log.Printf("failed to communicate with pprotein: %v", err)
		}
	}()

	c.Request().Header.Add("Content-Type", "application/json;charset=utf-8")
	return c.JSON(http.StatusOK, InitializeResponse{
		Language: "golang",
	})
}

func main() {
	go standalone.Integrate(":8888")

	e := echo.New()
	e.Debug = true
	e.Logger.SetLevel(echolog.DEBUG)
	e.Use(middleware.Logger())
	cookieStore := sessions.NewCookieStore(secret)
	cookieStore.Options.Domain = "*.u.isucon.local"
	e.Use(session.Middleware(cookieStore))
	// e.Use(middleware.Recover())
	e.Use(myMiddleware)

	// 初期化
	e.POST("/api/initialize", initializeHandler)

	// top
	e.GET("/api/tag", getTagHandler)
	e.GET("/api/user/:username/theme", getStreamerThemeHandler)

	// livestream
	// reserve livestream
	e.POST("/api/livestream/reservation", reserveLivestreamHandler)
	// list livestream
	e.GET("/api/livestream/search", searchLivestreamsHandler)
	e.GET("/api/livestream", getMyLivestreamsHandler)
	e.GET("/api/user/:username/livestream", getUserLivestreamsHandler)
	// get livestream
	e.GET("/api/livestream/:livestream_id", getLivestreamHandler)
	// get polling livecomment timeline
	e.GET("/api/livestream/:livestream_id/livecomment", getLivecommentsHandler)
	// ライブコメント投稿
	e.POST("/api/livestream/:livestream_id/livecomment", postLivecommentHandler)
	e.POST("/api/livestream/:livestream_id/reaction", postReactionHandler)
	e.GET("/api/livestream/:livestream_id/reaction", getReactionsHandler)

	// (配信者向け)ライブコメントの報告一覧取得API
	e.GET("/api/livestream/:livestream_id/report", getLivecommentReportsHandler)
	e.GET("/api/livestream/:livestream_id/ngwords", getNgwords)
	// ライブコメント報告
	e.POST("/api/livestream/:livestream_id/livecomment/:livecomment_id/report", reportLivecommentHandler)
	// 配信者によるモデレーション (NGワード登録)
	e.POST("/api/livestream/:livestream_id/moderate", moderateHandler)

	// livestream_viewersにINSERTするため必要
	// ユーザ視聴開始 (viewer)
	e.POST("/api/livestream/:livestream_id/enter", enterLivestreamHandler)
	// ユーザ視聴終了 (viewer)
	e.DELETE("/api/livestream/:livestream_id/exit", exitLivestreamHandler)

	// user
	e.POST("/api/register", registerHandler)
	e.POST("/api/login", loginHandler)
	e.GET("/api/user/me", getMeHandler)
	// フロントエンドで、配信予約のコラボレーターを指定する際に必要
	e.GET("/api/user/:username", getUserHandler)
	e.GET("/api/user/:username/statistics", getUserStatisticsHandler)
	e.GET("/api/user/:username/icon", getIconHandler)
	e.POST("/api/icon", postIconHandler)

	// stats
	// ライブ配信統計情報
	e.GET("/api/livestream/:livestream_id/statistics", getLivestreamStatisticsHandler)

	// 課金情報
	e.GET("/api/payment", GetPaymentResult)

	e.HTTPErrorHandler = errorResponseHandler

	// DB接続
	conn, err := connectDB(e.Logger)
	if err != nil {
		e.Logger.Errorf("failed to connect db: %v", err)
		os.Exit(1)
	}
	defer conn.Close()
	dbConn = conn

	subdomainAddr, ok := os.LookupEnv(powerDNSSubdomainAddressEnvKey)
	if !ok {
		e.Logger.Errorf("environ %s must be provided", powerDNSSubdomainAddressEnvKey)
		os.Exit(1)
	}
	powerDNSSubdomainAddress = subdomainAddr

	// HTTPサーバ起動
	listenAddr := net.JoinHostPort("", strconv.Itoa(listenPort))
	if err := e.Start(listenAddr); err != nil {
		e.Logger.Errorf("failed to start HTTP server: %v", err)
		os.Exit(1)
	}

	go func() {
		for {
			m := map[string]interface{}{}
			userActivity.Range(func(key, value interface{}) bool {
				m[fmt.Sprint(key)] = value
				return true
			})
			for trans, cnt := range m {
				log.Printf("%s: %d\n", trans, cnt.(int64))
			}
			time.Sleep(1 * time.Minute)
		}
	}()
}

type ErrorResponse struct {
	Error string `json:"error"`
}

func errorResponseHandler(err error, c echo.Context) {
	c.Logger().Errorf("error at %s: %+v", c.Path(), err)
	if he, ok := err.(*echo.HTTPError); ok {
		if e := c.JSON(he.Code, &ErrorResponse{Error: err.Error()}); e != nil {
			c.Logger().Errorf("%+v", e)
		}
		return
	}

	if e := c.JSON(http.StatusInternalServerError, &ErrorResponse{Error: err.Error()}); e != nil {
		c.Logger().Errorf("%+v", e)
	}
}
