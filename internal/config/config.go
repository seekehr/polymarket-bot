package config

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config represents the complete bot configuration
type Config struct {
	API          APIConfig          `yaml:"api"`
	Markets      MarketsConfig      `yaml:"markets"`
	Strategy     StrategyConfig     `yaml:"strategy"`
	Orders       OrdersConfig       `yaml:"orders"`
	Risk         RiskConfig         `yaml:"risk"`
	WebSocket    WebSocketConfig    `yaml:"websocket"`
	Performance  PerformanceConfig  `yaml:"performance"`
	Monitoring   MonitoringConfig   `yaml:"monitoring"`
	Backtesting  BacktestingConfig  `yaml:"backtesting"`
	Security     SecurityConfig     `yaml:"security"`
	Deployment   DeploymentConfig   `yaml:"deployment"`
}

type APIConfig struct {
	RESTEndpoint      string `yaml:"rest_endpoint"`
	WebSocketURL      string `yaml:"websocket_url"`
	APIKey            string `yaml:"api_key"`
	APISecret         string `yaml:"api_secret"`
	Passphrase        string `yaml:"passphrase"`
	TimeoutMS         int    `yaml:"timeout_ms"`
	KeepaliveEnabled  bool   `yaml:"keepalive_enabled"`
	MaxIdleConns      int    `yaml:"max_idle_conns"`
	MaxConnsPerHost   int    `yaml:"max_conns_per_host"`
}

type MarketsConfig struct {
	TargetMarketIDs []string `yaml:"target_market_ids"`
	MinLiquidity    float64  `yaml:"min_liquidity"`
}

type StrategyConfig struct {
	BuyBelow         float64 `yaml:"buy_below"`
	SellAbove        float64 `yaml:"sell_above"`
	Quantity         int     `yaml:"quantity"`
	MaxPositionSize  int     `yaml:"max_position_size"`
	MaxSlippagePct   float64 `yaml:"max_slippage_pct"`
	Mode             string  `yaml:"mode"`
}

type OrdersConfig struct {
	MaxOrdersPerSecond int    `yaml:"max_orders_per_second"`
	MaxOrdersPerMinute int    `yaml:"max_orders_per_minute"`
	BatchSize          int    `yaml:"batch_size"`
	BatchIntervalMS    int    `yaml:"batch_interval_ms"`
	DefaultOrderType   string `yaml:"default_order_type"`
	MaxOutstanding     int    `yaml:"max_outstanding"`
	MaxRetries         int    `yaml:"max_retries"`
	RetryDelayMS       int    `yaml:"retry_delay_ms"`
}

type RiskConfig struct {
	MaxDailyLoss          float64 `yaml:"max_daily_loss"`
	MaxLossPerTrade       float64 `yaml:"max_loss_per_trade"`
	AutoHaltOnLoss        bool    `yaml:"auto_halt_on_loss"`
	MaxExposurePerMarket  float64 `yaml:"max_exposure_per_market"`
}

type WebSocketConfig struct {
	ReadBufferSize       int  `yaml:"read_buffer_size"`
	WriteBufferSize      int  `yaml:"write_buffer_size"`
	PingIntervalSec      int  `yaml:"ping_interval_sec"`
	PongTimeoutSec       int  `yaml:"pong_timeout_sec"`
	ReconnectEnabled     bool `yaml:"reconnect_enabled"`
	ReconnectMaxAttempts int  `yaml:"reconnect_max_attempts"`
	ReconnectDelaySec    int  `yaml:"reconnect_delay_sec"`
	MessageBufferSize    int  `yaml:"message_buffer_size"`
}

type PerformanceConfig struct {
	WorkerCount     int   `yaml:"worker_count"`
	GCPercent       int   `yaml:"gc_percent"`
	CPUCores        []int `yaml:"cpu_cores"`
	StructPoolSize  int   `yaml:"struct_pool_size"`
}

type MonitoringConfig struct {
	Enabled          bool    `yaml:"enabled"`
	MetricsPort      int     `yaml:"metrics_port"`
	TrackLatency     bool    `yaml:"track_latency"`
	MaxLatencyMS     int     `yaml:"max_latency_ms"`
	MaxErrorRatePct  float64 `yaml:"max_error_rate_pct"`
	LogLevel         string  `yaml:"log_level"`
	LogFormat        string  `yaml:"log_format"`
}

type BacktestingConfig struct {
	Enabled        bool    `yaml:"enabled"`
	DataSource     string  `yaml:"data_source"`
	StartDate      string  `yaml:"start_date"`
	EndDate        string  `yaml:"end_date"`
	InitialBalance float64 `yaml:"initial_balance"`
}

type SecurityConfig struct {
	UseVault            bool   `yaml:"use_vault"`
	VaultEndpoint       string `yaml:"vault_endpoint"`
	VaultToken          string `yaml:"vault_token"`
	RemoteSignerEnabled bool   `yaml:"remote_signer_enabled"`
	RemoteSignerURL     string `yaml:"remote_signer_url"`
}

type DeploymentConfig struct {
	Region      string `yaml:"region"`
	Environment string `yaml:"environment"`
}

// Load reads and parses the configuration file
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	// Expand environment variables
	expanded := expandEnvVars(string(data))

	var cfg Config
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	// Validate configuration
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	return &cfg, nil
}

// expandEnvVars replaces ${VAR_NAME} with environment variable values
func expandEnvVars(s string) string {
	re := regexp.MustCompile(`\$\{([^}]+)\}`)
	return re.ReplaceAllStringFunc(s, func(match string) string {
		varName := strings.TrimSuffix(strings.TrimPrefix(match, "${"), "}")
		value := os.Getenv(varName)
		if value == "" {
			return match // Keep original if env var not set
		}
		return value
	})
}

// Validate checks if the configuration is valid
func (c *Config) Validate() error {
	if c.API.RESTEndpoint == "" {
		return fmt.Errorf("api.rest_endpoint is required")
	}
	if c.API.WebSocketURL == "" {
		return fmt.Errorf("api.websocket_url is required")
	}
	if len(c.Markets.TargetMarketIDs) == 0 {
		return fmt.Errorf("markets.target_market_ids must have at least one market")
	}
	if c.Strategy.BuyBelow <= 0 || c.Strategy.BuyBelow >= 1 {
		return fmt.Errorf("strategy.buy_below must be between 0 and 1")
	}
	if c.Strategy.SellAbove <= 0 || c.Strategy.SellAbove >= 1 {
		return fmt.Errorf("strategy.sell_above must be between 0 and 1")
	}
	if c.Strategy.BuyBelow >= c.Strategy.SellAbove {
		return fmt.Errorf("strategy.buy_below must be less than strategy.sell_above")
	}
	if c.Strategy.Quantity <= 0 {
		return fmt.Errorf("strategy.quantity must be positive")
	}
	if c.Orders.MaxOrdersPerSecond <= 0 {
		return fmt.Errorf("orders.max_orders_per_second must be positive")
	}
	if c.Risk.MaxDailyLoss < 0 {
		return fmt.Errorf("risk.max_daily_loss cannot be negative")
	}
	if c.WebSocket.MessageBufferSize <= 0 {
		return fmt.Errorf("websocket.message_buffer_size must be positive")
	}
	if c.Performance.WorkerCount <= 0 {
		return fmt.Errorf("performance.worker_count must be positive")
	}

	return nil
}
