/*
 * Copyright (c) 2021 Gilles Chehade <gilles@poolp.org>
 *
 * Permission to use, copy, modify, and distribute this software for any
 * purpose with or without fee is hereby granted, provided that the above
 * copyright notice and this permission notice appear in all copies.
 *
 * THE SOFTWARE IS PROVIDED "AS IS" AND THE AUTHOR DISCLAIMS ALL WARRANTIES
 * WITH REGARD TO THIS SOFTWARE INCLUDING ALL IMPLIED WARRANTIES OF
 * MERCHANTABILITY AND FITNESS. IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR
 * ANY SPECIAL, DIRECT, INDIRECT, OR CONSEQUENTIAL DAMAGES OR ANY DAMAGES
 * WHATSOEVER RESULTING FROM LOSS OF USE, DATA OR PROFITS, WHETHER IN AN
 * ACTION OF CONTRACT, NEGLIGENCE OR OTHER TORTIOUS ACTION, ARISING OUT OF
 * OR IN CONNECTION WITH THE USE OR PERFORMANCE OF THIS SOFTWARE.
 */

package sync

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/PlakarKorp/plakar/appcontext"
	"github.com/PlakarKorp/plakar/btree"
	"github.com/PlakarKorp/plakar/cmd/plakar/subcommands"
	"github.com/PlakarKorp/plakar/cmd/plakar/utils"
	"github.com/PlakarKorp/plakar/encryption"
	"github.com/PlakarKorp/plakar/objects"
	"github.com/PlakarKorp/plakar/repository"
	"github.com/PlakarKorp/plakar/resources"
	"github.com/PlakarKorp/plakar/snapshot"
	"github.com/PlakarKorp/plakar/snapshot/header"
	"github.com/PlakarKorp/plakar/snapshot/vfs"
	"github.com/PlakarKorp/plakar/storage"
	"github.com/google/uuid"
	"github.com/vmihailenco/msgpack/v5"
)

func init() {
	subcommands.Register("sync", parse_cmd_sync)
}

func parse_cmd_sync(ctx *appcontext.AppContext, repo *repository.Repository, args []string) (subcommands.Subcommand, error) {
	flags := flag.NewFlagSet("sync", flag.ExitOnError)
	flags.Usage = func() {
		fmt.Fprintf(flags.Output(), "Usage: %s [SNAPSHOT] to REPOSITORY\n", flags.Name())
		fmt.Fprintf(flags.Output(), "       %s [SNAPSHOT] from REPOSITORY\n", flags.Name())
		flags.PrintDefaults()
	}
	flags.Parse(args)

	syncSnapshotID := ""
	direction := ""
	peerRepositoryPath := ""

	args = flags.Args()
	switch len(args) {
	case 2:
		direction = args[0]
		peerRepositoryPath = args[1]
	case 3:
		syncSnapshotID = args[0]
		direction = args[1]
		peerRepositoryPath = args[2]

	default:
		return nil, fmt.Errorf("usage: sync [SNAPSHOT] to|from REPOSITORY")
	}

	if direction != "to" && direction != "from" && direction != "both" {
		return nil, fmt.Errorf("invalid direction, must be to, from or both")
	}

	peerStore, peerStoreSerializedConfig, err := storage.Open(peerRepositoryPath)
	if err != nil {
		return nil, err
	}

	peerStoreConfig, err := storage.NewConfigurationFromWrappedBytes(peerStoreSerializedConfig)
	if err != nil {
		return nil, err
	}

	var peerSecret []byte
	if peerStoreConfig.Encryption != nil {
		for {
			passphrase, err := utils.GetPassphrase("destination repository")
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s\n", err)
				continue
			}

			key, err := encryption.DeriveKey(peerStoreConfig.Encryption.KDFParams, passphrase)
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s\n", err)
				continue
			}
			if !encryption.VerifyCanary(peerStoreConfig.Encryption, key) {
				fmt.Fprintf(os.Stderr, "invalid passphrase\n")
				continue
			}
			peerSecret = key
			break
		}
	}

	peerCtx := appcontext.NewAppContextFrom(ctx)
	peerCtx.SetSecret(peerSecret)
	_, err = repository.New(peerCtx, peerStore, peerStoreSerializedConfig)
	if err != nil {
		return nil, err
	}

	return &Sync{
		SourceRepositoryLocation: repo.Location(),
		SourceRepositorySecret:   ctx.GetSecret(),
		PeerRepositoryLocation:   peerRepositoryPath,
		PeerRepositorySecret:     peerSecret,
		Direction:                direction,
		SnapshotPrefix:           syncSnapshotID,
	}, nil
}

type Sync struct {
	SourceRepositoryLocation string
	SourceRepositorySecret   []byte

	PeerRepositoryLocation string
	PeerRepositorySecret   []byte

	Direction string

	SnapshotPrefix string
}

func (cmd *Sync) Name() string {
	return "sync"
}

func (cmd *Sync) Execute(ctx *appcontext.AppContext, repo *repository.Repository) (int, error) {

	peerStore, peerStoreSerializedConfig, err := storage.Open(cmd.PeerRepositoryLocation)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: could not open repository: %s\n", cmd.PeerRepositoryLocation, err)
		return 1, err
	}

	peerCtx := appcontext.NewAppContextFrom(ctx)
	peerCtx.SetSecret(cmd.PeerRepositorySecret)
	peerRepository, err := repository.New(peerCtx, peerStore, peerStoreSerializedConfig)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: could not open repository: %s\n", peerStore.Location(), err)
		return 1, err
	}

	var srcRepository *repository.Repository
	var dstRepository *repository.Repository

	if cmd.Direction == "to" {
		srcRepository = repo
		dstRepository = peerRepository
	} else if cmd.Direction == "from" {
		srcRepository = peerRepository
		dstRepository = repo
	} else if cmd.Direction == "both" {
		srcRepository = repo
		dstRepository = peerRepository
	} else {
		fmt.Fprintf(os.Stderr, "%s: invalid direction, must be to, from or with\n", peerStore.Location())
		return 1, err
	}

	srcSnapshots, err := srcRepository.GetSnapshots()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: could not get snapshots from repository: %s\n", srcRepository.Location(), err)
		return 1, err
	}

	dstSnapshots, err := dstRepository.GetSnapshots()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: could not get snapshots list from repository: %s\n", dstRepository.Location(), err)
		return 1, err
	}

	srcSnapshotsMap := make(map[objects.MAC]struct{})
	dstSnapshotsMap := make(map[objects.MAC]struct{})

	for _, snapshotID := range srcSnapshots {
		srcSnapshotsMap[snapshotID] = struct{}{}
	}

	for _, snapshotID := range dstSnapshots {
		dstSnapshotsMap[snapshotID] = struct{}{}
	}

	srcSyncList := make([]objects.MAC, 0)

	srcLocateOptions := utils.NewDefaultLocateOptions()
	srcLocateOptions.Prefix = cmd.SnapshotPrefix
	srcSnapshotIDs, err := utils.LocateSnapshotIDs(srcRepository, srcLocateOptions)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: could not locate snapshots in repository: %s\n", srcRepository.Location(), err)
		return 1, err
	}

	for _, snapshotID := range srcSnapshotIDs {
		if _, exists := dstSnapshotsMap[snapshotID]; !exists {
			srcSyncList = append(srcSyncList, snapshotID)
		}
	}

	fmt.Printf("Synchronizing %d snapshots\n", len(srcSyncList))

	for _, snapshotID := range srcSyncList {
		err := synchronize(srcRepository, dstRepository, snapshotID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: could not synchronize snapshot %x from repository: %s\n", srcRepository.Location(), snapshotID, err)
		}
	}

	if cmd.Direction == "both" {
		dstSnapshotIDs, err := utils.LocateSnapshotIDs(dstRepository, srcLocateOptions)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: could not locate snapshots in repository: %s\n", srcRepository.Location(), err)
			return 1, err
		}

		dstSyncList := make([]objects.MAC, 0)
		for _, snapshotID := range dstSnapshotIDs {
			if _, exists := srcSnapshotsMap[snapshotID]; !exists {
				dstSyncList = append(dstSyncList, snapshotID)
			}
		}

		for _, snapshotID := range dstSyncList {
			err := synchronize(dstRepository, srcRepository, snapshotID)
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s: could not synchronize snapshot %x from repository: %s\n", dstRepository.Location(), snapshotID, err)
			}
		}
	}
	return 0, nil
}

func push(src *snapshot.Snapshot, dst *snapshot.Snapshot, mac objects.MAC, rtype resources.Type, data []byte) (bool, []byte, error) {
	var err error

	if dst.BlobExists(rtype, mac) {
		return true, nil, nil
	}

	if data == nil {
		data, err = src.GetBlob(rtype, mac)
		if err != nil {
			return false, nil, err
		}
	}
	return false, data, dst.PutBlob(rtype, mac, data)
}

func syncObject(src *snapshot.Snapshot, dst *snapshot.Snapshot, mac objects.MAC) error {
	found, objbytes, err := push(src, dst, mac, resources.RT_OBJECT, nil)
	if found || err != nil {
		return err
	}

	object, err := objects.NewObjectFromBytes(objbytes)
	if err != nil {
		return err
	}

	for _, chunk := range object.Chunks {
		_, _, err := push(src, dst, chunk.MAC, resources.RT_CHUNK, nil)
		if err != nil {
			return err
		}
	}
	return nil
}

func syncVFS(src *snapshot.Snapshot, dst *snapshot.Snapshot, fs *vfs.Filesystem, root objects.MAC) error {
	found, _, err := push(src, dst, root, resources.RT_VFS_BTREE, nil)
	if found || err != nil {
		return err
	}

	iter := fs.IterNodes()
	for iter.Next() {
		mac, node := iter.Current()

		bytes, err := msgpack.Marshal(node)
		if err != nil {
			return err
		}

		// we could actually skip all the nodes below this one
		// if it's present in the other side, but we're
		// missing an API to do so.
		found, _, err = push(src, dst, mac, resources.RT_VFS_NODE, bytes)
		if err != nil {
			return err
		}
		if found {
			continue
		}

		for _, entrymac := range node.Values {
			found, entrybytes, err := push(src, dst, entrymac, resources.RT_VFS_ENTRY, nil)
			if err != nil {
				return err
			}
			if found {
				continue
			}

			entry, err := vfs.EntryFromBytes(entrybytes)
			if err != nil {
				return err
			}

			if !entry.HasObject() {
				continue
			}

			if err := syncObject(src, dst, entry.Object); err != nil {
				return err
			}
		}
	}

	return nil
}

func syncErrors(src *snapshot.Snapshot, dst *snapshot.Snapshot, fs *vfs.Filesystem, root objects.MAC) error {
	found, _, err := push(src, dst, root, resources.RT_ERROR_BTREE, nil)
	if found || err != nil {
		return err
	}

	iter := fs.IterErrorNodes()
	for iter.Next() {
		mac, node := iter.Current()

		bytes, err := msgpack.Marshal(node)
		if err != nil {
			return err
		}

		// we could actually skip all the nodes below this one
		// if it's present in the other side, but we're
		// missing an API to do so.
		found, _, err := push(src, dst, mac, resources.RT_ERROR_NODE, bytes)
		if err != nil {
			return err
		}
		if found {
			continue
		}

		for _, errmac := range node.Values {
			_, _, err := push(src, dst, errmac, resources.RT_ERROR_ENTRY, nil)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func syncXattr(src *snapshot.Snapshot, dst *snapshot.Snapshot, fs *vfs.Filesystem, root objects.MAC) error {
	found, _, err := push(src, dst, root, resources.RT_XATTR_BTREE, nil)
	if found || err != nil {
		return err
	}

	iter := fs.XattrNodes()
	for iter.Next() {
		mac, node := iter.Current()

		bytes, err := msgpack.Marshal(node)
		if err != nil {
			return err
		}

		// we could actually skip all the nodes below this one
		// if it's present in the other side, but we're
		// missing an API to do so.
		found, _, err := push(src, dst, mac, resources.RT_XATTR_NODE, bytes)
		if err != nil {
			return err
		}
		if found {
			continue
		}

		for _, xattrmac := range node.Values {
			found, xbytes, err := push(src, dst, xattrmac, resources.RT_XATTR_ENTRY, nil)
			if err != nil {
				return err
			}
			if found {
				continue
			}

			xattr, err := vfs.XattrFromBytes(xbytes)
			if err != nil {
				return err
			}

			if err := syncObject(src, dst, xattr.Object); err != nil {
				return err
			}
		}
	}

	return nil
}

func syncIndex(repo *repository.Repository, src *snapshot.Snapshot, dst *snapshot.Snapshot, index *header.Index) error {
	switch index.Name {
	case "content-type":
		found, serialized, err := push(src, dst, index.Value, resources.RT_BTREE_ROOT, nil)
		if found || err != nil {
			return err
		}

		store := repository.NewRepositoryStore[string, objects.MAC](repo, resources.RT_BTREE_NODE)
		tree, err := btree.Deserialize(bytes.NewReader(serialized), store, strings.Compare)
		if err != nil {
			return err
		}

		it := tree.IterDFS()
		for it.Next() {
			mac, node := it.Current()

			bytes, err := msgpack.Marshal(node)
			if err != nil {
				return err
			}

			_, _, err = push(src, dst, mac, resources.RT_BTREE_NODE, bytes)
			if err != nil {
				return err
			}
		}

	default:
		return fmt.Errorf("don't know how to sync the index %s of type %s",
			index.Name, index.Type)
	}

	return nil
}

func synchronize(srcRepository *repository.Repository, dstRepository *repository.Repository, snapshotId objects.MAC) error {
	srcSnapshot, err := snapshot.Load(srcRepository, snapshotId)
	if err != nil {
		return err
	}
	defer srcSnapshot.Close()

	dstSnapshot, err := snapshot.New(dstRepository)
	if err != nil {
		return err
	}
	defer dstSnapshot.Close()

	// overwrite the header, we want to keep the original snapshot info
	dstSnapshot.Header = srcSnapshot.Header

	if srcSnapshot.Header.Identity.Identifier != uuid.Nil {
		_, _, err := push(srcSnapshot, dstSnapshot, srcSnapshot.Header.Identifier,
			resources.RT_SIGNATURE, nil)
		if err != nil {
			return err
		}
	}

	source := srcSnapshot.Header.GetSource(0)
	fs, err := srcSnapshot.Filesystem()
	if err != nil {
		return err
	}
	if err := syncVFS(srcSnapshot, dstSnapshot, fs, source.VFS.Root); err != nil {
		return err
	}
	if err := syncErrors(srcSnapshot, dstSnapshot, fs, source.VFS.Errors); err != nil {
		return err
	}
	if err := syncXattr(srcSnapshot, dstSnapshot, fs, source.VFS.Xattrs); err != nil {
		return err
	}

	for i := range source.Indexes {
		if err := syncIndex(srcRepository, srcSnapshot, dstSnapshot, &source.Indexes[i]); err != nil {
			return err
		}
	}

	return dstSnapshot.Commit()
}
