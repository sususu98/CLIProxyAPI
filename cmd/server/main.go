// Package main provides the entry point for the CLI Proxy API server.
// This server acts as a proxy that provides OpenAI/Gemini/Claude compatible API interfaces
// for CLI models, allowing CLI models to be used with tools and libraries designed for standard AI APIs.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	configaccess "github.com/router-for-me/CLIProxyAPI/v6/internal/access/config_access"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/cmd"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/store"
	_ "github.com/router-for-me/CLIProxyAPI/v6/internal/translator"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v6/sdk/auth"
	log "github.com/sirupsen/logrus"
)

var (
	Version           = "dev"
	Commit            = "none"
	BuildDate         = "unknown"
	DefaultConfigPath = ""
)

// init initializes the shared logger setup.
func init() {
	logging.SetupBaseLogger()
}

// main is the entry point of the application.
// It parses command-line flags, loads configuration, and starts the appropriate
// service based on the provided flags (login, codex-login, or server mode).
func main() {
	fmt.Printf("CLIProxyAPI Version: %s, Commit: %s, BuiltAt: %s\n", Version, Commit, BuildDate)

	// Command-line flags to control the application's behavior.
	var login bool
	var codexLogin bool
	var claudeLogin bool
	var qwenLogin bool
	var iflowLogin bool
	var noBrowser bool
	var projectID string
	var configPath string
	var password string

	// Define command-line flags for different operation modes.
	flag.BoolVar(&login, "login", false, "Login Google Account")
	flag.BoolVar(&codexLogin, "codex-login", false, "Login to Codex using OAuth")
	flag.BoolVar(&claudeLogin, "claude-login", false, "Login to Claude using OAuth")
	flag.BoolVar(&qwenLogin, "qwen-login", false, "Login to Qwen using OAuth")
	flag.BoolVar(&iflowLogin, "iflow-login", false, "Login to iFlow using OAuth")
	flag.BoolVar(&noBrowser, "no-browser", false, "Don't open browser automatically for OAuth")
	flag.StringVar(&projectID, "project_id", "", "Project ID (Gemini only, not required)")
	flag.StringVar(&configPath, "config", DefaultConfigPath, "Configure File Path")
	flag.StringVar(&password, "password", "", "")

	flag.CommandLine.Usage = func() {
		out := flag.CommandLine.Output()
		_, _ = fmt.Fprintf(out, "Usage of %s\n", os.Args[0])
		flag.CommandLine.VisitAll(func(f *flag.Flag) {
			if f.Name == "password" {
				return
			}
			s := fmt.Sprintf("  -%s", f.Name)
			name, unquoteUsage := flag.UnquoteUsage(f)
			if name != "" {
				s += " " + name
			}
			if len(s) <= 4 {
				s += "	"
			} else {
				s += "\n    "
			}
			if unquoteUsage != "" {
				s += unquoteUsage
			}
			if f.DefValue != "" && f.DefValue != "false" && f.DefValue != "0" {
				s += fmt.Sprintf(" (default %s)", f.DefValue)
			}
			_, _ = fmt.Fprint(out, s+"\n")
		})
	}

	// Parse the command-line flags.
	flag.Parse()

	// Core application variables.
	var err error
	var cfg *config.Config
	var isCloudDeploy bool
	var (
		gitStoreLocalPath string
		useGitStore       bool
		gitStoreRemoteURL string
		gitStoreUser      string
		gitStorePassword  string
		gitStoreInst      *store.GitTokenStore
		gitStoreRoot      string
	)

	wd, err := os.Getwd()
	if err != nil {
		log.Fatalf("failed to get working directory: %v", err)
	}

	lookupEnv := func(keys ...string) (string, bool) {
		for _, key := range keys {
			if value, ok := os.LookupEnv(key); ok {
				if trimmed := strings.TrimSpace(value); trimmed != "" {
					return trimmed, true
				}
			}
		}
		return "", false
	}
	if value, ok := lookupEnv("GITSTORE_GIT_URL", "gitstore_git_url"); ok {
		useGitStore = true
		gitStoreRemoteURL = value
	}
	if value, ok := lookupEnv("GITSTORE_GIT_USERNAME", "gitstore_git_username"); ok {
		gitStoreUser = value
	}
	if value, ok := lookupEnv("GITSTORE_GIT_TOKEN", "gitstore_git_token"); ok {
		gitStorePassword = value
	}
	if value, ok := lookupEnv("GITSTORE_LOCAL_PATH", "gitstore_local_path"); ok {
		gitStoreLocalPath = value
	}

	// Check for cloud deploy mode only on first execution
	// Read env var name in uppercase: DEPLOY
	deployEnv := os.Getenv("DEPLOY")
	if deployEnv == "cloud" {
		isCloudDeploy = true
	}

	// Determine and load the configuration file.
	// If gitstore is configured, load from the cloned repository; otherwise use the provided path or default.
	var configFilePath string
	if useGitStore {
		if gitStoreLocalPath == "" {
			gitStoreLocalPath = wd
		}
		gitStoreRoot = filepath.Join(gitStoreLocalPath, "remote")
		authDir := filepath.Join(gitStoreRoot, "auth")
		gitStoreInst = store.NewGitTokenStore(gitStoreRemoteURL, gitStoreUser, gitStorePassword)
		gitStoreInst.SetBaseDir(authDir)
		if errRepo := gitStoreInst.EnsureRepository(); errRepo != nil {
			log.Fatalf("failed to prepare git token store: %v", errRepo)
		}
		configFilePath = gitStoreInst.ConfigPath()
		if configFilePath == "" {
			configFilePath = filepath.Join(gitStoreRoot, "config", "config.yaml")
		}
		if _, statErr := os.Stat(configFilePath); errors.Is(statErr, fs.ErrNotExist) {
			examplePath := filepath.Join(wd, "config.example.yaml")
			if _, errExample := os.Stat(examplePath); errExample != nil {
				log.Fatalf("failed to find template config file: %v", errExample)
			}
			if errCopy := misc.CopyConfigTemplate(examplePath, configFilePath); errCopy != nil {
				log.Fatalf("failed to bootstrap git-backed config: %v", errCopy)
			}
			if errCommit := gitStoreInst.CommitConfig(context.Background()); errCommit != nil {
				log.Fatalf("failed to commit initial git-backed config: %v", errCommit)
			}
			log.Infof("git-backed config initialized from template: %s", configFilePath)
		} else if statErr != nil {
			log.Fatalf("failed to inspect git-backed config: %v", statErr)
		}
		cfg, err = config.LoadConfigOptional(configFilePath, isCloudDeploy)
		if err == nil {
			cfg.AuthDir = gitStoreInst.AuthDir()
			log.Infof("git-backed token store enabled, repository path: %s", gitStoreRoot)
		}
	} else if configPath != "" {
		configFilePath = configPath
		cfg, err = config.LoadConfigOptional(configPath, isCloudDeploy)
	} else {
		wd, err = os.Getwd()
		if err != nil {
			log.Fatalf("failed to get working directory: %v", err)
		}
		configFilePath = filepath.Join(wd, "config.yaml")
		cfg, err = config.LoadConfigOptional(configFilePath, isCloudDeploy)
	}
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}
	if cfg == nil {
		cfg = &config.Config{}
	}

	// In cloud deploy mode, check if we have a valid configuration
	var configFileExists bool
	if isCloudDeploy {
		if info, errStat := os.Stat(configFilePath); errStat != nil {
			// Don't mislead: API server will not start until configuration is provided.
			log.Info("Cloud deploy mode: No configuration file detected; standing by for configuration")
			configFileExists = false
		} else if info.IsDir() {
			log.Info("Cloud deploy mode: Config path is a directory; standing by for configuration")
			configFileExists = false
		} else if cfg.Port == 0 {
			// LoadConfigOptional returns empty config when file is empty or invalid.
			// Config file exists but is empty or invalid; treat as missing config
			log.Info("Cloud deploy mode: Configuration file is empty or invalid; standing by for valid configuration")
			configFileExists = false
		} else {
			log.Info("Cloud deploy mode: Configuration file detected; starting service")
			configFileExists = true
		}
	}
	usage.SetStatisticsEnabled(cfg.UsageStatisticsEnabled)

	if err = logging.ConfigureLogOutput(cfg.LoggingToFile); err != nil {
		log.Fatalf("failed to configure log output: %v", err)
	}

	log.Infof("CLIProxyAPI Version: %s, Commit: %s, BuiltAt: %s", Version, Commit, BuildDate)

	// Set the log level based on the configuration.
	util.SetLogLevel(cfg)

	if resolvedAuthDir, errResolveAuthDir := util.ResolveAuthDir(cfg.AuthDir); errResolveAuthDir != nil {
		log.Fatalf("failed to resolve auth directory: %v", errResolveAuthDir)
	} else {
		cfg.AuthDir = resolvedAuthDir
	}

	// Create login options to be used in authentication flows.
	options := &cmd.LoginOptions{
		NoBrowser: noBrowser,
	}

	// Register the shared token store once so all components use the same persistence backend.
	if useGitStore {
		sdkAuth.RegisterTokenStore(gitStoreInst)
	} else {
		sdkAuth.RegisterTokenStore(sdkAuth.NewFileTokenStore())
	}

	// Register built-in access providers before constructing services.
	configaccess.Register()

	// Handle different command modes based on the provided flags.

	if login {
		// Handle Google/Gemini login
		cmd.DoLogin(cfg, projectID, options)
	} else if codexLogin {
		// Handle Codex login
		cmd.DoCodexLogin(cfg, options)
	} else if claudeLogin {
		// Handle Claude login
		cmd.DoClaudeLogin(cfg, options)
	} else if qwenLogin {
		cmd.DoQwenLogin(cfg, options)
	} else if iflowLogin {
		cmd.DoIFlowLogin(cfg, options)
	} else {
		// In cloud deploy mode without config file, just wait for shutdown signals
		if isCloudDeploy && !configFileExists {
			// No config file available, just wait for shutdown
			cmd.WaitForCloudDeploy()
			return
		}
		// Start the main proxy service
		cmd.StartService(cfg, configFilePath, password)
	}
}
