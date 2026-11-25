-- Set once today's shares have become the family's selections. Until then
-- the plan never removes anything, so a fresh install cannot empty the
-- Kindle profiles. Databases that already have selections were bootstrapped.
INSERT INTO settings (key, value)
SELECT 'bootstrapped', '1' WHERE EXISTS (SELECT 1 FROM selections);
