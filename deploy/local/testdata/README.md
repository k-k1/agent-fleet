# deploy/local/testdata

Frozen inputs for the offline tests in `deploy/local/`. Never edit them by hand.

- `00-network.v0.34.1.yaml` is `git show v0.34.1:deploy/aws/ecs/cfn/00-network.yaml`, byte for
  byte. `cfn-network-upgrade-test.py` diffs it against the current template to pin the change
  sets of the v0.34.x upgrade (#2763). It is vendored because CI checks out one commit without
  tags; where the tag is present the test compares the copy with it.
