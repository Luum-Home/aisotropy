package semantic

import (
	"context"
	"net"
	"sort"
	"sync"
	"testing"

	"github.com/qdrant/go-client/qdrant"
	"google.golang.org/grpc"
)

// fakeQdrantServer is the shared in-memory state behind a minimal, in-process
// implementation of the three Qdrant gRPC services QdrantStore actually calls
// (Qdrant/Collections/Points). It exists so qdrant.go's REAL code (OpenQdrant,
// ensureCollection, Upsert, Search, Delete, DeleteBySource, PruneTurns,
// EraseScope, scrollIDs, Close) runs over real gRPC wire traffic — filters
// built by qdrant.go are parsed and evaluated here exactly as a live Qdrant
// node would, so the constitutional payload-filter-during-scan behavior is
// exercised against the actual filter-construction logic, not a
// reimplementation the test controls end to end. A live node is still the
// authority (qdrant_manual_test.go, `-tags manual`); this fake only removes
// the network dependency for unit coverage.
//
// gRPC's Collections and Points services both declare a `Delete` RPC with
// different request/response types, so the three services cannot be
// implemented by ONE Go receiver type (a single method named Delete cannot
// satisfy two different signatures). The state lives here; three thin
// per-service adapter types below implement one gRPC service interface each
// and delegate to this shared state.
type fakeQdrantServer struct {
	mu          sync.Mutex
	collections map[string]bool
	points      map[string]map[string]fakePoint // collection -> point id -> point

	// Error injection: when non-nil, the named RPC returns it instead of
	// running its normal logic. Sub-tests set these directly (same package).
	failCollectionExists error
	failCreateCollection error
	failCreateFieldIndex error
	failUpsert           error
	failQuery            error
	failDelete           error
	failScroll           error
}

type fakePoint struct {
	id      string
	payload map[string]*qdrant.Value
}

func newFakeQdrantServer() *fakeQdrantServer {
	return &fakeQdrantServer{
		collections: map[string]bool{},
		points:      map[string]map[string]fakePoint{},
	}
}

// startFakeQdrant starts fake on a loopback TCP port, registers it against all
// three services, and returns the (host, port) to point a QdrantConfig at.
// The server and listener are stopped via t.Cleanup.
func startFakeQdrant(t *testing.T, fake *fakeQdrantServer) (host string, port int) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("fake qdrant listen: %v", err)
	}
	srv := grpc.NewServer()
	qdrant.RegisterQdrantServer(srv, fakeQdrantHealthService{s: fake})
	qdrant.RegisterCollectionsServer(srv, fakeQdrantCollectionsService{s: fake})
	qdrant.RegisterPointsServer(srv, fakeQdrantPointsService{s: fake})
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() {
		srv.Stop()
		_ = lis.Close()
	})
	addr := lis.Addr().(*net.TCPAddr)
	return "127.0.0.1", addr.Port
}

// fakeQdrantHealthService implements QdrantServer (just HealthCheck, the
// version-compatibility probe qdrant-client runs once per NewClient).
type fakeQdrantHealthService struct {
	qdrant.UnimplementedQdrantServer
	s *fakeQdrantServer
}

func (h fakeQdrantHealthService) HealthCheck(_ context.Context, _ *qdrant.HealthCheckRequest) (*qdrant.HealthCheckReply, error) {
	// Version is intentionally unpinned to the real client's build version: a
	// mismatch only logs a warning (qdrant-client's IsCompatible), it is never
	// fatal, so the exact string does not matter for this fake.
	return &qdrant.HealthCheckReply{Title: "fake-qdrant", Version: "1.0.0"}, nil
}

// fakeQdrantCollectionsService implements CollectionsServer (CollectionExists
// + Create — the two RPCs ensureCollection issues).
type fakeQdrantCollectionsService struct {
	qdrant.UnimplementedCollectionsServer
	s *fakeQdrantServer
}

func (c fakeQdrantCollectionsService) CollectionExists(_ context.Context, req *qdrant.CollectionExistsRequest) (*qdrant.CollectionExistsResponse, error) {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	if c.s.failCollectionExists != nil {
		return nil, c.s.failCollectionExists
	}
	return &qdrant.CollectionExistsResponse{Result: &qdrant.CollectionExists{Exists: c.s.collections[req.GetCollectionName()]}}, nil
}

func (c fakeQdrantCollectionsService) Create(_ context.Context, req *qdrant.CreateCollection) (*qdrant.CollectionOperationResponse, error) {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	if c.s.failCreateCollection != nil {
		return nil, c.s.failCreateCollection
	}
	c.s.collections[req.GetCollectionName()] = true
	if c.s.points[req.GetCollectionName()] == nil {
		c.s.points[req.GetCollectionName()] = map[string]fakePoint{}
	}
	return &qdrant.CollectionOperationResponse{Result: true}, nil
}

// fakeQdrantPointsService implements PointsServer (CreateFieldIndex, Upsert,
// Query, Delete, Scroll — everything qdrant.go's point-level operations use).
type fakeQdrantPointsService struct {
	qdrant.UnimplementedPointsServer
	s *fakeQdrantServer
}

func (p fakeQdrantPointsService) CreateFieldIndex(_ context.Context, _ *qdrant.CreateFieldIndexCollection) (*qdrant.PointsOperationResponse, error) {
	if p.s.failCreateFieldIndex != nil {
		return nil, p.s.failCreateFieldIndex
	}
	return &qdrant.PointsOperationResponse{Result: &qdrant.UpdateResult{}}, nil
}

func (p fakeQdrantPointsService) Upsert(_ context.Context, req *qdrant.UpsertPoints) (*qdrant.PointsOperationResponse, error) {
	p.s.mu.Lock()
	defer p.s.mu.Unlock()
	if p.s.failUpsert != nil {
		return nil, p.s.failUpsert
	}
	coll := p.s.points[req.GetCollectionName()]
	if coll == nil {
		coll = map[string]fakePoint{}
		p.s.points[req.GetCollectionName()] = coll
	}
	for _, pt := range req.GetPoints() {
		id := pt.GetId().GetUuid()
		coll[id] = fakePoint{id: id, payload: pt.GetPayload()}
	}
	return &qdrant.PointsOperationResponse{Result: &qdrant.UpdateResult{}}, nil
}

func (p fakeQdrantPointsService) Query(_ context.Context, req *qdrant.QueryPoints) (*qdrant.QueryResponse, error) {
	p.s.mu.Lock()
	defer p.s.mu.Unlock()
	if p.s.failQuery != nil {
		return nil, p.s.failQuery
	}
	coll := p.s.points[req.GetCollectionName()]
	must := req.GetFilter().GetMust()
	var out []*qdrant.ScoredPoint
	for _, pt := range coll {
		if !fakeMatchesFilter(pt, must) {
			continue
		}
		out = append(out, &qdrant.ScoredPoint{
			Id:      qdrant.NewIDUUID(pt.id),
			Payload: pt.payload,
			Score:   1, // ranking is not under test here; only filter correctness is
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GetId().GetUuid() < out[j].GetId().GetUuid() })
	if lim := req.GetLimit(); lim > 0 && uint64(len(out)) > lim {
		out = out[:lim]
	}
	return &qdrant.QueryResponse{Result: out}, nil
}

func (p fakeQdrantPointsService) Delete(_ context.Context, req *qdrant.DeletePoints) (*qdrant.PointsOperationResponse, error) {
	p.s.mu.Lock()
	defer p.s.mu.Unlock()
	if p.s.failDelete != nil {
		return nil, p.s.failDelete
	}
	coll := p.s.points[req.GetCollectionName()]
	must := req.GetPoints().GetFilter().GetMust()
	for id, pt := range coll {
		if fakeMatchesFilter(pt, must) {
			delete(coll, id)
		}
	}
	return &qdrant.PointsOperationResponse{Result: &qdrant.UpdateResult{}}, nil
}

func (p fakeQdrantPointsService) Scroll(_ context.Context, req *qdrant.ScrollPoints) (*qdrant.ScrollResponse, error) {
	p.s.mu.Lock()
	defer p.s.mu.Unlock()
	if p.s.failScroll != nil {
		return nil, p.s.failScroll
	}
	coll := p.s.points[req.GetCollectionName()]
	must := req.GetFilter().GetMust()
	var matched []fakePoint
	for _, pt := range coll {
		if fakeMatchesFilter(pt, must) {
			matched = append(matched, pt)
		}
	}
	if ob := req.GetOrderBy(); ob != nil {
		key := ob.GetKey()
		desc := ob.GetDirection() == qdrant.Direction_Desc
		sort.Slice(matched, func(i, j int) bool {
			vi := matched[i].payload[key].GetIntegerValue()
			vj := matched[j].payload[key].GetIntegerValue()
			if desc {
				return vi > vj
			}
			return vi < vj
		})
	} else {
		sort.Slice(matched, func(i, j int) bool { return matched[i].id < matched[j].id })
	}
	if lim := req.GetLimit(); lim > 0 && uint32(len(matched)) > lim {
		matched = matched[:lim]
	}
	result := make([]*qdrant.RetrievedPoint, 0, len(matched))
	for _, pt := range matched {
		result = append(result, &qdrant.RetrievedPoint{Id: qdrant.NewIDUUID(pt.id)})
	}
	return &qdrant.ScrollResponse{Result: result}, nil
}

// fakeMatchesFilter evaluates a Must (AND) condition list against a stored
// point — the same condition vocabulary qdrant.go builds (NewMatch,
// NewMatchKeywords, NewRange, NewHasID) for its isolation filter and
// maintenance scans.
func fakeMatchesFilter(p fakePoint, must []*qdrant.Condition) bool {
	for _, c := range must {
		if !fakeMatchesCondition(p, c) {
			return false
		}
	}
	return true
}

func fakeMatchesCondition(p fakePoint, c *qdrant.Condition) bool {
	if hasID := c.GetHasId(); hasID != nil {
		for _, id := range hasID.GetHasId() {
			if id.GetUuid() == p.id {
				return true
			}
		}
		return false
	}
	fc := c.GetField()
	if fc == nil {
		return true
	}
	val := p.payload[fc.GetKey()]
	if m := fc.GetMatch(); m != nil {
		if kws := m.GetKeywords(); kws != nil {
			for _, want := range kws.GetStrings() {
				if val.GetStringValue() == want {
					return true
				}
			}
			return false
		}
		return val.GetStringValue() == m.GetKeyword()
	}
	if r := fc.GetRange(); r != nil {
		v := float64(val.GetIntegerValue())
		if r.Lt != nil && !(v < *r.Lt) {
			return false
		}
		if r.Gt != nil && !(v > *r.Gt) {
			return false
		}
		if r.Gte != nil && !(v >= *r.Gte) {
			return false
		}
		if r.Lte != nil && !(v <= *r.Lte) {
			return false
		}
		return true
	}
	return true
}
