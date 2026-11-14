package datastore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/eclipse-iofog/edgelet-kube/internal/bundle"
	"github.com/k3s-io/kine/pkg/drivers/generic"
	"github.com/k3s-io/kine/pkg/drivers/sqlite"
	"github.com/k3s-io/kine/pkg/endpoint"
	kinetls "github.com/k3s-io/kine/pkg/tls"
)

const (
	dbDirName  = "db"
	dbFileName = "state.db"
	socketName = "kine.sock"

	// agentDB is the agent process database. This runtime does not open it.
	agentDB = "/var/lib/edgelet/edgelet.db"
)

// Config selects the data directory and the datastore endpoint.
// An empty Endpoint uses SQLite under DataDir.
type Config struct {
	// DataDir is the runtime data directory. Empty uses the default data directory.
	DataDir string
	// Endpoint is the datastore endpoint. Empty selects SQLite.
	Endpoint string
	// CAFile is the backend CA. On nats:// the client uses TLS to this CA.
	CAFile string
	// CertFile is the optional client certificate.
	CertFile string
	// KeyFile is the optional client key.
	KeyFile string
	// NATSCreds is the NATS credentials file. It is injected as credsFile.
	NATSCreds string
	// SecretsEncryption encrypts secrets at rest when true.
	// A first boot writes the encryption config before the database file.
	// The apiserver is not started.
	SecretsEncryption bool
}

// Store is an open kine datastore.
type Store struct {
	// Path is the SQLite database file.
	Path string
	// Endpoint is the address kine is serving.
	Endpoint string
	// LeaderElect reports whether this backend wants leader election.
	LeaderElect bool
	// EncryptionConfig is the secrets encryption file.
	// Empty tells the caller not to set encryption-provider-config.
	EncryptionConfig string

	cancel context.CancelFunc
	wg     *sync.WaitGroup
}

// StateDB is the SQLite file used when the datastore endpoint is empty.
func StateDB(dataDir string) string {
	if dataDir == "" {
		dataDir = bundle.DefaultDataDir
	}
	return filepath.Join(dataDir, dbDirName, dbFileName)
}

// Open starts kine. An empty endpoint opens SQLite at StateDB with kine's WAL
// settings and leaves leader election off. Any other endpoint is handed to
// kine after NATS credentials and CA rules are applied. An empty endpoint
// does not start NATS. Secrets encryption is decided before the database
// file is created. Open does not start kube-apiserver and does not open
// the agent database.
func Open(ctx context.Context, cfg Config) (*Store, error) {
	enc, err := PrepareEncryption(cfg.DataDir, cfg.SecretsEncryption)
	if err != nil {
		return nil, err
	}
	store, err := open(ctx, cfg)
	if err != nil {
		return nil, err
	}
	store.EncryptionConfig = enc.ProviderConfig
	return store, nil
}

func open(ctx context.Context, cfg Config) (*Store, error) {
	if strings.TrimSpace(cfg.Endpoint) == "" {
		dbPath := StateDB(cfg.DataDir)
		if err := rejectAgentDB(dbPath); err != nil {
			return nil, err
		}
		dsn := "sqlite://" + dbPath + "?" + sqlite.DefaultParams
		return listen(ctx, cfg.DataDir, dsn, dbPath, kinetls.Config{}, "open sqlite datastore")
	}
	dsn, tlsCfg, err := prepare(cfg)
	if err != nil {
		return nil, err
	}
	scheme, _, _ := strings.Cut(dsn, "://")
	if scheme == "sqlite" {
		dbPath := sqliteFile(dsn)
		if err := rejectAgentDB(dbPath); err != nil {
			return nil, err
		}
		if dbPath != "" && dbPath != ":memory:" {
			if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
				return nil, fmt.Errorf("create datastore directory: %w", err)
			}
		}
		return listen(ctx, cfg.DataDir, dsn, dbPath, tlsCfg, "open sqlite datastore")
	}
	return listen(ctx, cfg.DataDir, dsn, "", tlsCfg, "open datastore")
}

func listen(ctx context.Context, dataDir, dsn, dbPath string, tlsCfg kinetls.Config, errPrefix string) (*Store, error) {
	dir := dataDir
	if dir == "" {
		dir = bundle.DefaultDataDir
	}
	dbDir := filepath.Join(dir, dbDirName)
	if localSocket(dsn) {
		if err := os.MkdirAll(dbDir, 0o700); err != nil {
			return nil, fmt.Errorf("create datastore directory: %w", err)
		}
	}
	sock := filepath.Join(dbDir, socketName)
	ctx, cancel := context.WithCancel(ctx)
	wg := &sync.WaitGroup{}
	// Intervals match kine's built-in server defaults.
	serving, err := endpoint.Listen(ctx, endpoint.Config{
		WaitGroup:           wg,
		Listener:            "unix://" + sock,
		Endpoint:            dsn,
		BackendTLSConfig:    tlsCfg,
		NotifyInterval:      5 * time.Second,
		EmulatedETCDVersion: "3.6.11",
		CompactTimeout:      5 * time.Second,
		CompactMinRetain:    1000,
		CompactBatchSize:    1000,
		PollBatchSize:       500,
		PollInterval:        time.Second,
		ConnectionPoolConfig: generic.ConnectionPoolConfig{
			MaxIdleTime: 2 * time.Minute,
		},
	})
	if err != nil {
		cancel()
		return nil, fmt.Errorf("%s: %w", errPrefix, err)
	}
	return &Store{
		Path:        dbPath,
		Endpoint:    strings.Join(serving.Endpoints, ","),
		LeaderElect: serving.LeaderElect,
		cancel:      cancel,
		wg:          wg,
	}, nil
}

// localSocket reports whether kine serves this endpoint on a local socket.
// External etcd is passed through and does not listen.
func localSocket(dsn string) bool {
	scheme, _, _ := strings.Cut(dsn, "://")
	switch scheme {
	case "http", "https", "unix", "unixs":
		return false
	default:
		return true
	}
}

func rejectAgentDB(path string) error {
	if path == "" {
		return nil
	}
	clean := filepath.Clean(path)
	if clean == agentDB || filepath.Base(clean) == filepath.Base(agentDB) {
		return fmt.Errorf("refusing agent database %s", path)
	}
	return nil
}

// Close stops kine and closes the database.
func (s *Store) Close() {
	if s == nil || s.cancel == nil {
		return
	}
	s.cancel()
	s.cancel = nil
	if s.wg != nil {
		s.wg.Wait()
	}
}
