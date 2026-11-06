-- Neither the service nor a v0.10 replica, which still serves while the migrations run, names this
-- table. The drop also removes the triggers of its foreign key from generations, so it locks both
-- tables, for the catalog change only.
DROP TABLE generation_payloads;
