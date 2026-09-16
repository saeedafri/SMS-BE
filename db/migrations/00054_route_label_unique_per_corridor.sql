-- +goose Up

-- A corridor cannot hold two routes with the same label.
--
-- The label is how an operator tells two paths to the same carrier apart, and
-- the console asks them to choose one by it — "disable Videocon direct" is not
-- an instruction anybody can carry out when two rows answer to it. The contract
-- has documented this 409 since routes were added; nothing enforced it.
--
-- A corridor is one country and channel, not one carrier: the same label under
-- two carriers in the same corridor is just as ambiguous on screen.
CREATE UNIQUE INDEX routes_corridor_label ON routes (country, channel, label);

-- +goose Down
DROP INDEX routes_corridor_label;
