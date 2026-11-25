-- The name a kids profile has on the parent dashboard (e.g. りか). Family
-- members can be registered by this name; the reconciler fills in
-- kids_profile_id once it has seen which ID the name belongs to.
ALTER TABLE members ADD COLUMN kids_profile_name TEXT;
CREATE UNIQUE INDEX members_kids_profile_name ON members(kids_profile_name);
