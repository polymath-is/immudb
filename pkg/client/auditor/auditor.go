/*
Copyright 2019-2020 vChain, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package auditor

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/codenotary/immudb/pkg/client/rootservice"
	"google.golang.org/grpc/metadata"

	"github.com/codenotary/immudb/pkg/api/schema"
	"github.com/codenotary/immudb/pkg/auth"
	"github.com/codenotary/immudb/pkg/client"
	"github.com/codenotary/immudb/pkg/client/cache"
	"github.com/codenotary/immudb/pkg/client/timestamp"
	"github.com/codenotary/immudb/pkg/logger"
	"github.com/golang/protobuf/ptypes/empty"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"
)

// Auditor the auditor interface
type Auditor interface {
	Run(interval time.Duration, singleRun bool, stopc <-chan struct{}, donec chan<- struct{}) error
}

// AuditNotificationConfig holds the URL and credentials used to publish audit
// result to ledger compliance.
type AuditNotificationConfig struct {
	URL            string
	Username       string
	Password       string
	RequestTimeout time.Duration

	publishFunc func(*http.Request) (*http.Response, error)
}

type defaultAuditor struct {
	index              uint64
	databaseIndex      int
	logger             logger.Logger
	serverAddress      string
	dialOptions        []grpc.DialOption
	history            cache.HistoryCache
	ts                 client.TimestampService
	username           []byte
	databases          []string
	password           []byte
	auditDatabases     []string
	auditSignature     string
	notificationConfig AuditNotificationConfig
	serviceClient      schema.ImmuServiceClient
	uuidProvider       rootservice.UUIDProvider

	slugifyRegExp *regexp.Regexp
	updateMetrics func(string, string, bool, bool, bool, *schema.Root, *schema.Root)
}

// DefaultAuditor creates initializes a default auditor implementation
func DefaultAuditor(
	interval time.Duration,
	serverAddress string,
	dialOptions *[]grpc.DialOption,
	username string,
	passwordBase64 string,
	auditDatabases []string,
	auditSignature string,
	notificationConfig AuditNotificationConfig,
	serviceClient schema.ImmuServiceClient,
	uuidProvider rootservice.UUIDProvider,
	history cache.HistoryCache,
	updateMetrics func(string, string, bool, bool, bool, *schema.Root, *schema.Root),
	log logger.Logger) (Auditor, error) {

	switch auditSignature {
	case "validate":
	case "ignore":
	case "":
	default:
		return nil, errors.New("auditSignature allowed values are 'validate' or 'ignore'")
	}

	password, err := auth.DecodeBase64Password(passwordBase64)
	if err != nil {
		return nil, err
	}

	dt, _ := timestamp.NewDefaultTimestamp()

	slugifyRegExp, _ := regexp.Compile(`[^a-zA-Z0-9\-_]+`)

	httpClient := &http.Client{Timeout: notificationConfig.RequestTimeout}
	notificationConfig.publishFunc = httpClient.Do

	return &defaultAuditor{
		0,
		0,
		log,
		serverAddress,
		*dialOptions,
		history,
		client.NewTimestampService(dt),
		[]byte(username),
		nil,
		[]byte(password),
		auditDatabases,
		auditSignature,
		notificationConfig,
		serviceClient,
		uuidProvider,
		slugifyRegExp,
		updateMetrics,
	}, nil
}

func (a *defaultAuditor) Run(
	interval time.Duration,
	singleRun bool,
	stopc <-chan struct{},
	donec chan<- struct{},
) (err error) {
	defer func() { donec <- struct{}{} }()
	a.logger.Infof("starting auditor with a %s interval ...", interval)

	if singleRun {
		err = a.audit()
	} else {
		err = repeat(interval, stopc, a.audit)
		if err != nil {
			return err
		}
	}
	a.logger.Infof("auditor stopped")
	return err
}

func (a *defaultAuditor) audit() error {
	start := time.Now()
	a.index++
	a.logger.Infof("audit #%d started @ %s", a.index, start)

	verified := true
	checked := false
	withError := false
	serverID := "unknown"
	var prevRoot *schema.Root
	var root *schema.Root
	defer func() {
		a.updateMetrics(
			serverID, a.serverAddress, checked, withError, verified, prevRoot, root)
	}()

	// returning an error would completely stop the auditor process
	var noErr error

	ctx := context.Background()
	loginResponse, err := a.serviceClient.Login(ctx, &schema.LoginRequest{
		User:     a.username,
		Password: a.password,
	})
	if err != nil {
		a.logger.Errorf("error logging in with user %s: %v", a.username, err)
		withError = true
		return noErr
	}
	defer a.serviceClient.Logout(ctx, &empty.Empty{})

	md := metadata.Pairs("authorization", loginResponse.Token)
	ctx = metadata.NewOutgoingContext(context.Background(), md)

	//check if we have cycled through the list of databases
	if a.databaseIndex == len(a.databases) {
		//if we have reached the end get a fresh list of dbs that belong to the user
		dbs, err := a.serviceClient.DatabaseList(ctx, &emptypb.Empty{})
		if err != nil {
			a.logger.Errorf("error getting a list of databases %v", err)
			withError = true
			return noErr
		}
		a.databases = nil
		for _, db := range dbs.Databases {
			dbMustBeAudited := len(a.auditDatabases) <= 0
			for _, dbPrefix := range a.auditDatabases {
				if strings.HasPrefix(db.Databasename, dbPrefix) {
					dbMustBeAudited = true
					break
				}
			}
			if dbMustBeAudited {
				a.databases = append(a.databases, db.Databasename)
			}
		}
		a.databaseIndex = 0
		if len(a.databases) <= 0 {
			a.logger.Errorf(
				"audit #%d aborted: no databases to audit found after (re)loading the list of databases",
				a.index)
			withError = true
			return noErr
		}
		a.logger.Infof(
			"audit #%d - list of databases to audit has been (re)loaded - %d database(s) found: %v",
			a.index, len(a.databases), a.databases)
	}
	dbName := a.databases[a.databaseIndex]
	resp, err := a.serviceClient.UseDatabase(ctx, &schema.Database{
		Databasename: dbName,
	})
	if err != nil {
		a.logger.Errorf("error selecting database %s: %v", dbName, err)
		withError = true
		return noErr
	}

	md = metadata.Pairs("authorization", resp.Token)
	ctx = metadata.NewOutgoingContext(context.Background(), md)

	a.logger.Infof("audit #%d - auditing database %s\n", a.index, dbName)
	a.databaseIndex++

	root, err = a.serviceClient.CurrentRoot(ctx, &empty.Empty{})
	if err != nil {
		a.logger.Errorf("error getting current root: %v", err)
		withError = true
		return noErr
	}

	if a.auditSignature == "validate" {
		if okSig, err := root.CheckSignature(); err != nil || !okSig {
			a.logger.Errorf(
				"audit #%d aborted: could not verify signature on server root at %s @ %s",
				a.index, serverID, a.serverAddress)
			withError = true
			return noErr
		}
	}

	isEmptyDB := len(root.GetRoot()) == 0 && root.GetIndex() == 0

	serverID = a.getServerID(ctx)
	prevRoot, err = a.history.Get(serverID, dbName)
	if err != nil {
		a.logger.Errorf(err.Error())
		withError = true
		return noErr
	}
	if prevRoot != nil {
		if isEmptyDB {
			a.logger.Errorf(
				"audit #%d aborted: database is empty on server %s @ %s, "+
					"but locally a previous root exists with hash %x at index %d",
				a.index, serverID, a.serverAddress, prevRoot.GetRoot(), prevRoot.GetIndex())
			withError = true
			return noErr
		}
		proof, err := a.serviceClient.Consistency(ctx, &schema.Index{
			Index: prevRoot.GetIndex(),
		})
		if err != nil {
			a.logger.Errorf(
				"error fetching consistency proof for previous root %d: %v",
				prevRoot.GetIndex(), err)
			withError = true
			return noErr
		}
		verified =
			proof.Verify(schema.Root{Payload: &schema.RootIndex{Index: prevRoot.GetIndex(), Root: prevRoot.GetRoot()}})
		firstRoot := proof.FirstRoot
		// proof.FirstRoot is empty if check fails
		if !verified && len(firstRoot) == 0 {
			firstRoot = prevRoot.GetRoot()
		}
		a.logger.Infof("audit #%d result:\n db: %s, consistent:	%t\n"+
			"  firstRoot:	%x at index: %d\n  secondRoot:	%x at index: %d",
			a.index, dbName, verified,
			firstRoot, proof.First, proof.SecondRoot, proof.Second)
		root = &schema.Root{
			Payload: &schema.RootIndex{Index: proof.Second, Root: proof.SecondRoot},
			// TODO OGG: here the signature from proof should be set, but proof
			// does not have roots Signatures yet.
			// NOTE: the signature from the root obtained with CurrentRoot call above
			// might belong to an older root if the server root changed between the
			// CurrentRoot and Consistency calls above, that's why we don't set that.
			Signature: nil,
		}
		checked = true
		// publish audit notification
		if len(a.notificationConfig.URL) > 0 {
			err := a.publishAuditNotification(
				dbName,
				time.Now(),
				!verified,
				&Root{
					Index: proof.First,
					Hash:  fmt.Sprintf("%x", firstRoot),
					Signature: Signature{
						Signature: base64.StdEncoding.EncodeToString(prevRoot.GetSignature().GetSignature()),
						PublicKey: base64.StdEncoding.EncodeToString(prevRoot.GetSignature().GetPublicKey()),
					},
				},
				&Root{
					Index: proof.Second,
					Hash:  fmt.Sprintf("%x", proof.SecondRoot),
					Signature: Signature{
						Signature: base64.StdEncoding.EncodeToString(root.GetSignature().GetSignature()),
						PublicKey: base64.StdEncoding.EncodeToString(root.GetSignature().GetPublicKey()),
					},
				},
			)
			if err != nil {
				a.logger.Errorf(
					"error publishing audit notification for db %s: %v", dbName, err)
			} else {
				a.logger.Infof(
					"audit notification for db %s has been published at %s",
					dbName, a.notificationConfig.URL)
			}
		}
	} else if isEmptyDB {
		a.logger.Warningf("audit #%d canceled: database is empty on server %s @ %s",
			a.index, serverID, a.serverAddress)
		return noErr
	}

	if !verified {
		a.logger.Warningf(
			"audit #%d detected possible tampering of db %s remote root (at index %d) "+
				"so it will not overwrite the previous local root (at index %d)",
			a.index, dbName, root.GetIndex(), prevRoot.GetIndex())
	} else if prevRoot == nil || root.GetIndex() != prevRoot.GetIndex() {
		if err := a.history.Set(root, serverID, dbName); err != nil {
			a.logger.Errorf(err.Error())
			return noErr
		}
	}
	a.logger.Infof("audit #%d finished in %s @ %s",
		a.index, time.Since(start), time.Now().Format(time.RFC3339Nano))

	return noErr
}

// Signature ...
type Signature struct {
	Signature string `json:"signature"`
	PublicKey string `json:"public_key"`
}

// Root ...
type Root struct {
	Index     uint64    `json:"index" validate:"required"`
	Hash      string    `json:"hash" validate:"required"`
	Signature Signature `json:"signature" validate:"required"`
}

// AuditNotificationRequest ...
type AuditNotificationRequest struct {
	Username     string    `json:"username" validate:"required"`
	Password     string    `json:"password" validate:"required"`
	DB           string    `json:"db" validate:"required"`
	RunAt        time.Time `json:"run_at" validate:"required" example:"2020-11-13T00:53:42+01:00"`
	Tampered     bool      `json:"tampered"`
	PreviousRoot *Root     `json:"previous_root"`
	CurrentRoot  *Root     `json:"current_root"`
}

func (a *defaultAuditor) publishAuditNotification(
	db string,
	runAt time.Time,
	tampered bool,
	prevRoot *Root,
	currRoot *Root) error {

	payload := AuditNotificationRequest{
		Username:     a.notificationConfig.Username,
		Password:     a.notificationConfig.Password,
		DB:           db,
		RunAt:        runAt,
		Tampered:     tampered,
		PreviousRoot: prevRoot,
		CurrentRoot:  currRoot,
	}

	reqBody, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequest("POST", a.notificationConfig.URL, bytes.NewBuffer(reqBody))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := a.notificationConfig.publishFunc(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated, http.StatusAccepted, http.StatusNoContent:
	default:
		respBody, _ := ioutil.ReadAll(resp.Body)
		return fmt.Errorf(
			"POST %s request with body %s: "+
				"got unexpected response status %s with response body %s",
			a.notificationConfig.URL, reqBody,
			resp.Status, respBody)
	}

	return nil
}

func (a *defaultAuditor) getServerID(
	ctx context.Context,
) string {
	serverID, err := a.uuidProvider.CurrentUUID(ctx)
	if err != nil {
		if err != rootservice.ErrNoServerUuid {
			a.logger.Errorf("error getting server UUID: %v", err)
		} else {
			a.logger.Warningf(err.Error())
		}
	}
	if serverID == "" {
		serverID = strings.ReplaceAll(
			strings.ReplaceAll(a.serverAddress, ".", "-"),
			":", "_")
		serverID = a.slugifyRegExp.ReplaceAllString(serverID, "")
		a.logger.Debugf(
			"the current immudb server @ %s will be identified as %s",
			a.serverAddress, serverID)
	}
	return serverID
}

// repeat executes f every interval until stopc is closed or f returns an error.
// It executes f once right after being called.
func repeat(
	interval time.Duration,
	stopc <-chan struct{},
	f func() error,
) error {
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		if err := f(); err != nil {
			return err
		}
		select {
		case <-stopc:
			return nil
		case <-tick.C:
		}
	}
}
