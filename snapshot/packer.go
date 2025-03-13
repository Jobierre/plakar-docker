package snapshot

import (
	"bytes"
	"context"
	"fmt"
	"hash"
	"io"
	"runtime"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/PlakarKorp/plakar/objects"
	"github.com/PlakarKorp/plakar/packfile"
	"github.com/PlakarKorp/plakar/resources"
	"github.com/PlakarKorp/plakar/versioning"
)

type PackerManager struct {
	snapshot       *Snapshot
	inflightMACs   sync.Map
	packerChan     chan interface{}
	packerChanDone chan struct{}
}

func NewPackerManager(snapshot *Snapshot) *PackerManager {
	return &PackerManager{
		snapshot:       snapshot,
		inflightMACs:   sync.Map{},
		packerChan:     make(chan interface{}, runtime.NumCPU()*2+1),
		packerChanDone: make(chan struct{}),
	}
}

func (mgr *PackerManager) Run() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	packerResultChan := make(chan *Packer, runtime.NumCPU())

	flusherGroup, _ := errgroup.WithContext(ctx)
	flusherGroup.Go(func() error {
		for packer := range packerResultChan {
			if packer == nil || packer.Size() == 0 {
				continue
			}

			if err := mgr.snapshot.PutPackfile(packer); err != nil {
				return fmt.Errorf("failed to flush packer: %w", err)
			}

			for _, record := range packer.Packfile.Index {
				mgr.inflightMACs.Delete(record.MAC)
			}
		}
		return nil
	})

	workerGroup, workerCtx := errgroup.WithContext(ctx)
	for i := 0; i < runtime.NumCPU(); i++ {
		workerGroup.Go(func() error {
			var packer *Packer

			for {
				select {
				case <-workerCtx.Done():
					return workerCtx.Err()
				case msg, ok := <-mgr.packerChan:
					if !ok {
						if packer != nil && packer.Size() > 0 {
							packerResultChan <- packer
						}
						return nil
					}

					pm, ok := msg.(*PackerMsg)
					if !ok {
						return fmt.Errorf("unexpected message type")
					}

					if _, exists := mgr.inflightMACs.Load(pm.MAC); exists {
						continue
					}

					if packer == nil {
						packer = NewPacker(mgr.snapshot.Repository().GetMACHasher())
					}

					mgr.inflightMACs.Store(pm.MAC, struct{}{})

					if !packer.AddBlobIfNotExists(pm.Type, pm.Version, pm.MAC, pm.Data, pm.Flags) {
						continue
					}

					if packer.Size() > uint32(mgr.snapshot.repository.Configuration().Packfile.MaxSize) {
						packerResultChan <- packer
						packer = nil
					}
				}
			}
		})
	}

	// Wait for workers to finish.
	if err := workerGroup.Wait(); err != nil {
		mgr.snapshot.Logger().Error("Worker group error: %s", err)
		cancel() // Propagate cancellation.
	}

	// Close the result channel and wait for the flusher to finish.
	close(packerResultChan)
	if err := flusherGroup.Wait(); err != nil {
		mgr.snapshot.Logger().Error("Flusher group error: %s", err)
	}

	// Signal completion.
	mgr.packerChanDone <- struct{}{}
	close(mgr.packerChanDone)
}

func (mgr *PackerManager) Wait() {
	close(mgr.packerChan)
	<-mgr.packerChanDone
}

type PackerMsg struct {
	Timestamp time.Time
	Type      resources.Type
	Version   versioning.Version
	MAC       objects.MAC
	Data      []byte
	Flags     uint32
}

type Packer struct {
	Blobs    map[resources.Type]map[[32]byte][]byte
	Packfile *packfile.PackFile
}

func NewPacker(hasher hash.Hash) *Packer {
	blobs := make(map[resources.Type]map[[32]byte][]byte)
	for _, Type := range resources.Types() {
		blobs[Type] = make(map[[32]byte][]byte)
	}
	return &Packer{
		Packfile: packfile.New(hasher),
		Blobs:    blobs,
	}
}

func (packer *Packer) AddBlobIfNotExists(Type resources.Type, version versioning.Version, mac [32]byte, data []byte, flags uint32) bool {
	if _, ok := packer.Blobs[Type]; !ok {
		packer.Blobs[Type] = make(map[[32]byte][]byte)
	}
	if _, ok := packer.Blobs[Type][mac]; ok {
		return false
	}
	packer.Blobs[Type][mac] = data
	packer.Packfile.AddBlob(Type, version, mac, data, flags)
	return true
}

func (packer *Packer) Size() uint32 {
	return packer.Packfile.Size()
}

func (packer *Packer) Types() []resources.Type {
	ret := make([]resources.Type, 0, len(packer.Blobs))
	for k := range packer.Blobs {
		ret = append(ret, k)
	}
	return ret
}

func (snap *Snapshot) PutBlob(Type resources.Type, mac [32]byte, data []byte) error {
	snap.Logger().Trace("snapshot", "%x: PutBlob(%s, %064x) len=%d", snap.Header.GetIndexShortID(), Type, mac, len(data))

	if _, exists := snap.packerManager.inflightMACs.Load(mac); exists {
		return nil
	}

	encodedReader, err := snap.repository.Encode(bytes.NewReader(data))
	if err != nil {
		return err
	}

	encoded, err := io.ReadAll(encodedReader)
	if err != nil {
		return err
	}

	snap.packerManager.packerChan <- &PackerMsg{Type: Type, Version: versioning.GetCurrentVersion(Type), Timestamp: time.Now(), MAC: mac, Data: encoded}
	return nil
}

func (snap *Snapshot) GetBlob(Type resources.Type, mac [32]byte) ([]byte, error) {
	snap.Logger().Trace("snapshot", "%x: GetBlob(%s, %x)", snap.Header.GetIndexShortID(), Type, mac)

	// XXX: Temporary workaround, once the state API changes to get from both sources (delta+aggregated state)
	// we can remove this hack.
	if snap.deltaState != nil {
		loc, exists, err := snap.deltaState.GetSubpartForBlob(Type, mac)
		if err != nil {
			return nil, err
		}

		if exists {
			rd, err := snap.repository.GetPackfileBlob(loc)
			if err != nil {
				return nil, err
			}

			return io.ReadAll(rd)
		}
	}

	// Not found in delta, let's lookup the localstate
	rd, err := snap.repository.GetBlob(Type, mac)
	if err != nil {
		return nil, err
	}

	return io.ReadAll(rd)
}

func (snap *Snapshot) BlobExists(Type resources.Type, mac [32]byte) bool {
	snap.Logger().Trace("snapshot", "%x: CheckBlob(%s, %064x)", snap.Header.GetIndexShortID(), Type, mac)

	// XXX: Same here, remove this workaround when state API changes.
	if snap.deltaState != nil {
		if _, exists := snap.packerManager.inflightMACs.Load(mac); exists {
			return true
		}
		return snap.deltaState.BlobExists(Type, mac) || snap.repository.BlobExists(Type, mac)
	} else {
		return snap.repository.BlobExists(Type, mac)
	}
}

func (snap *Snapshot) PutBlobIfNotExists(Type resources.Type, mac [32]byte, data []byte) error {
	snap.Logger().Trace("snapshot", "%x: PutBlobIfNotExists(%s, %064x) len=%d", snap.Header.GetIndexShortID(), Type, mac, len(data))
	if snap.BlobExists(Type, mac) {
		return nil
	}
	return snap.PutBlob(Type, mac, data)
}
