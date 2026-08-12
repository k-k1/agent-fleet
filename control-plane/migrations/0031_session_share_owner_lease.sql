-- Distributed owner lease for serializing approved Agent operations with share mutations.
CREATE TABLE session_share_owner_lease (
    owner_membership_id TEXT PRIMARY KEY REFERENCES membership(id) ON DELETE CASCADE,
    operation_id        TEXT NOT NULL,
    expires_at          TEXT NOT NULL,
    updated_at          TEXT NOT NULL
);
CREATE INDEX idx_session_share_owner_lease_expiry ON session_share_owner_lease(expires_at);
