ALTER TABLE proof_change_candidate_mappings
    ADD COLUMN compare_deployed boolean NOT NULL DEFAULT false;
