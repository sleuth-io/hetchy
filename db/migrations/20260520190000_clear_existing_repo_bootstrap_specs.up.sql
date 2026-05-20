-- The bootstrap artifact contract now requires stop.sh and lessons.md,
-- and the setup/start split has new runtime semantics. Existing saved
-- specs were generated under the old contract, so force repos to
-- bootstrap again after this deployment.
DELETE FROM repo_setup_specs;
