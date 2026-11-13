-- Each list numbers its own updates. One global sequence let two writers
-- take seq 10 and 11 and commit 11 first; a catch-up in between reported
-- head 11 and never fetched 10. The counter lives on the list row, which
-- every writer locks, so seqs are assigned and committed in order and are
-- dense per list. Existing seqs stay as they are; each list counts on from
-- its highest one.
ALTER TABLE groups ADD COLUMN update_seq BIGINT NOT NULL DEFAULT 0;
UPDATE groups g SET update_seq = u.max_seq
    FROM (SELECT list_id, MAX(seq) AS max_seq FROM list_updates GROUP BY list_id) u
    WHERE u.list_id = g.id;

ALTER TABLE list_updates ALTER COLUMN seq DROP DEFAULT;
DROP SEQUENCE list_updates_seq_seq;
