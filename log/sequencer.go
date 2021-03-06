// Runs sequencing operations
package log

import (
	"fmt"
	"github.com/golang/glog"
	"github.com/google/trillian"
	"github.com/google/trillian/crypto"
	"github.com/google/trillian/merkle"
	"github.com/google/trillian/storage"
	"github.com/google/trillian/util"
)

// Sequencer instances are responsible for integrating new leaves into a log.
// Leaves will be assigned unique sequence numbers when they are processed.
// There is no strong ordering guarantee but in general entries will be processed
// in order of submission to the log.
type Sequencer struct {
	hasher     merkle.TreeHasher
	timeSource util.TimeSource
	logStorage storage.LogStorage
	keyManager crypto.KeyManager
}

// maxTreeDepth sets an upper limit on the size of Log trees.
// TODO(al): We actually can't go beyond 2^63 entries becuase we use int64s,
//           but we need to calculate tree depths from a multiple of 8 due to
//           the subtrees.
const maxTreeDepth = 64

// CurrentRootExpiredFunc examines a signed log root and decides if it has expired with respect
// to a max age duration and a given time source
// TODO(Martin2112): This is all likely to go away when we switch to application STHs
type CurrentRootExpiredFunc func(trillian.SignedLogRoot) bool

func NewSequencer(hasher merkle.TreeHasher, timeSource util.TimeSource, logStorage storage.LogStorage, km crypto.KeyManager) *Sequencer {
	return &Sequencer{hasher, timeSource, logStorage, km}
}

// TODO: This currently doesn't use the batch api for fetching the required nodes. This
// would be more efficient but requires refactoring.
func (s Sequencer) buildMerkleTreeFromStorageAtRoot(root trillian.SignedLogRoot, tx storage.TreeTX) (*merkle.CompactMerkleTree, error) {
	mt, err := merkle.NewCompactMerkleTreeWithState(s.hasher, root.TreeSize, func(depth int, index int64) (trillian.Hash, error) {
		nodeId, err := storage.NewNodeIDForTreeCoords(int64(depth), index, maxTreeDepth)
		if err != nil {
			glog.Warningf("Failed to create nodeID: %v", err)
			return nil, err
		}
		nodes, err := tx.GetMerkleNodes(root.TreeRevision, []storage.NodeID{nodeId})

		if err != nil {
			glog.Warningf("Failed to get merkle nodes: %s", err)
			return nil, err
		}

		// We expect to get exactly one node here
		if nodes == nil || len(nodes) != 1 {
			return nil, fmt.Errorf("Did not retrieve one node while loading CompactMerkleTree, got %#v for ID %s@%d", nodes, nodeId.String(), root.TreeRevision)
		}

		return nodes[0].Hash, nil
	}, root.RootHash)

	return mt, err
}

func (s Sequencer) buildNodesFromNodeMap(nodeMap map[string]storage.Node, newVersion int64) ([]storage.Node, error) {
	targetNodes := make([]storage.Node, len(nodeMap), len(nodeMap))
	i := 0
	for _, node := range nodeMap {
		node.NodeRevision = newVersion
		targetNodes[i] = node
		i++
	}
	return targetNodes, nil
}

func (s Sequencer) sequenceLeaves(mt *merkle.CompactMerkleTree, leaves []trillian.LogLeaf) (map[string]storage.Node, []int64, error) {
	nodeMap := make(map[string]storage.Node)
	sequenceNumbers := make([]int64, 0, len(leaves))

	// Update the tree state and sequence the leaves, tracking the node updates that need to be
	// made and assign sequence numbers to the new leaves
	for _, leaf := range leaves {
		seq := mt.AddLeafHash(leaf.LeafHash, func(depth int, index int64, hash trillian.Hash) {
			nodeId, err := storage.NewNodeIDForTreeCoords(int64(depth), index, maxTreeDepth)
			if err != nil {
				return
			}
			nodeMap[nodeId.String()] = storage.Node{
				NodeID: nodeId,
				Hash:   hash,
			}
		})
		// store leaf hash in the merkle tree too:
		leafNodeID, err := storage.NewNodeIDForTreeCoords(0, seq, maxTreeDepth)
		if err != nil {
			return nil, nil, err
		}
		nodeMap[leafNodeID.String()] = storage.Node{
			NodeID: leafNodeID,
			Hash:   leaf.LeafHash,
		}

		sequenceNumbers = append(sequenceNumbers, seq)
	}

	return nodeMap, sequenceNumbers, nil
}

func (s Sequencer) initMerkleTreeFromStorage(currentRoot trillian.SignedLogRoot, tx storage.LogTX) (*merkle.CompactMerkleTree, error) {
	if currentRoot.TreeSize == 0 {
		return merkle.NewCompactMerkleTree(s.hasher), nil
	}

	// Initialize the compact tree state to match the latest root in the database
	return s.buildMerkleTreeFromStorageAtRoot(currentRoot, tx)
}

func (s Sequencer) signRoot(root trillian.SignedLogRoot) (trillian.DigitallySigned, error) {
	signer, err := s.keyManager.Signer()

	if err != nil {
		glog.Warningf("key manager failed to create crypto.Signer: %v", err)
		return trillian.DigitallySigned{}, err
	}

	// TODO(Martin2112): Signature algorithm shouldn't be fixed here
	trillianSigner := crypto.NewTrillianSigner(s.hasher.Hasher, trillian.SignatureAlgorithm_ECDSA, signer)

	signature, err := trillianSigner.SignLogRoot(root)

	if err != nil {
		glog.Warningf("signer failed to sign root: %v", err)
		return trillian.DigitallySigned{}, err
	}

	return signature, nil
}

// SequenceBatch wraps up all the operations needed to take a batch of queued leaves
// and integrate them into the tree.
// TODO(Martin2112): Can possibly improve by deferring a function that attempts to rollback,
// which will fail if the tx was committed. Should only do this if we can hide the details of
// the underlying storage transactions and it doesn't create other problems.
func (s Sequencer) SequenceBatch(limit int, expiryFunc CurrentRootExpiredFunc) (int, error) {
	tx, err := s.logStorage.Begin()

	if err != nil {
		glog.Warningf("Sequencer failed to start tx: %s", err)
		return 0, err
	}

	leaves, err := tx.DequeueLeaves(limit)

	if err != nil {
		glog.Warningf("Sequencer failed to dequeue leaves: %s", err)
		tx.Rollback()
		return 0, err
	}

	// Get the latest known root from storage
	currentRoot, err := tx.LatestSignedLogRoot()

	if err != nil {
		glog.Warningf("Sequencer failed to get latest root: %s", err)
		tx.Rollback()
		return 0, err
	}

	// TODO(al): Have a better detection mechanism for there being no stored root.
	if currentRoot.RootHash == nil {
		glog.Warning("Fresh log - no previous TreeHeads exist.")
	}

	// There might be no work to be done. But we possibly still need to create an STH if the
	// current one is too old. If there's work to be done then we'll be creating a root anyway.
	if len(leaves) == 0 {
		// We have nothing to integrate into the tree
		tx.Commit()
		if expiryFunc(currentRoot) {
			// Current root is too old, sign one. Will use a new TX, safe as we have no writes
			// pending in this one.
			return 0, s.SignRoot()
		}
		return 0, nil
	}

	merkleTree, err := s.initMerkleTreeFromStorage(currentRoot, tx)

	if err != nil {
		tx.Rollback()
		return 0, err
	}

	// We've done all the reads, can now do the updates.
	// TODO: This relies on us being the only process updating the map, which isn't enforced yet
	// though the schema should now prevent multiple STHs being inserted with the same revision
	// number so it should not be possible for colliding updates to commit.
	newVersion := tx.WriteRevision()
	if got, want := newVersion, currentRoot.TreeRevision+int64(1); got != want {
		tx.Rollback()
		return 0, fmt.Errorf("got writeRevision of %d, but expected %d", got, want)
	}

	// Assign leaf sequence numbers and collate node updates
	nodeMap, sequenceNumbers, err := s.sequenceLeaves(merkleTree, leaves)
	if err != nil {
		tx.Rollback()
		return 0, err
	}

	if len(sequenceNumbers) != len(leaves) {
		panic(fmt.Sprintf("Sequencer returned %d sequence numbers for %d leaves", len(sequenceNumbers),
			len(leaves)))
	}

	for index, _ := range sequenceNumbers {
		leaves[index].SequenceNumber = sequenceNumbers[index]
	}

	// Write the new sequence numbers to the leaves in the DB
	err = tx.UpdateSequencedLeaves(leaves)

	if err != nil {
		glog.Warningf("Sequencer failed to update sequenced leaves: %s", err)
		tx.Rollback()
		return 0, err
	}

	// Build objects for the nodes to be updated. Because we deduped via the map each
	// node can only be created / updated once in each tree revision and they cannot
	// conflict when we do the storage update.
	targetNodes, err := s.buildNodesFromNodeMap(nodeMap, newVersion)

	if err != nil {
		// probably an internal error with map building, unexpected
		glog.Warningf("Failed to build target nodes in sequencer: %s", err)
		tx.Rollback()
		return 0, err
	}

	// Now insert or update the nodes affected by the above, at the new tree version
	err = tx.SetMerkleNodes(targetNodes)

	if err != nil {
		glog.Warningf("Sequencer failed to set merkle nodes: %s", err)
		tx.Rollback()
		return 0, err
	}

	// Create the log root ready for signing
	newLogRoot := trillian.SignedLogRoot{
		RootHash:       merkleTree.CurrentRoot(),
		TimestampNanos: s.timeSource.Now().UnixNano(),
		TreeSize:       merkleTree.Size(),
		LogId:          currentRoot.LogId,
		TreeRevision:   newVersion,
	}

	// Hash and sign the root, update it with the signature
	signature, err := s.signRoot(newLogRoot)

	if err != nil {
		glog.Warningf("signer failed to sign root: %v", err)
		tx.Rollback()
		return 0, err
	}

	newLogRoot.Signature = &signature

	err = tx.StoreSignedLogRoot(newLogRoot)

	if err != nil {
		glog.Warningf("failed to write updated tree root: %s", err)
		tx.Rollback()
		return 0, err
	}

	// The batch is now fully sequenced and we're done
	if err := tx.Commit(); err != nil {
		return 0, err
	}

	return len(leaves), nil
}

// SignRoot wraps up all the operations for creating a new log signed root.
func (s Sequencer) SignRoot() error {
	tx, err := s.logStorage.Begin()

	if err != nil {
		glog.Warningf("signer failed to start tx: %s", err)
		return err
	}

	// Get the latest known root from storage
	currentRoot, err := tx.LatestSignedLogRoot()

	if err != nil {
		glog.Warningf("signer failed to get latest root: %s", err)
		tx.Rollback()
		return err
	}

	// Initialize a Merkle Tree from the state in storage. This should fail if the tree is
	// in a corrupt state.
	merkleTree, err := s.initMerkleTreeFromStorage(currentRoot, tx)

	if err != nil {
		tx.Rollback()
		return err
	}

	// Build the updated root, ready for signing
	newLogRoot := trillian.SignedLogRoot{
		RootHash:       merkleTree.CurrentRoot(),
		TimestampNanos: s.timeSource.Now().UnixNano(),
		TreeSize:       merkleTree.Size(),
		LogId:          currentRoot.LogId,
		TreeRevision:   currentRoot.TreeRevision + 1,
	}

	// Hash and sign the root
	signature, err := s.signRoot(newLogRoot)

	if err != nil {
		glog.Warningf("signer failed to sign root: %v", err)
		tx.Rollback()
		return err
	}

	newLogRoot.Signature = &signature

	// Store the new root and we're done
	if err := tx.StoreSignedLogRoot(newLogRoot); err != nil {
		glog.Warningf("signer failed to write updated root: %v", err)
		tx.Rollback()
		return err
	}

	return tx.Commit()
}
