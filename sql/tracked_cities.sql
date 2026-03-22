-- Reference table: cities to track on Polymarket
-- One scheduler reads this table (WHERE active = TRUE) instead of having one scheduler per city.
--
-- Run via:
--   bq query --project_id=fg-polylabs --use_legacy_sql=false < sql/tracked_cities.sql

CREATE TABLE IF NOT EXISTS `fg-polylabs.weather.tracked_cities` (
  city          STRING  NOT NULL OPTIONS (description = 'Normalized slug form used in Polymarket event slugs, e.g. london, new-york, nyc'),
  display_name  STRING  NOT NULL OPTIONS (description = 'Human-readable city name, e.g. London, New York'),
  timezone      STRING  NOT NULL OPTIONS (description = 'IANA timezone identifier, e.g. Europe/London'),
  active        BOOL    NOT NULL OPTIONS (description = 'Set to FALSE to pause tracking without removing the row'),
  added_date    DATE    NOT NULL OPTIONS (description = 'Date this city was added to tracking'),
  notes         STRING           OPTIONS (description = 'Optional free-text notes')
);

-- Seed with currently tracked cities
-- To add a city:   INSERT INTO ... VALUES (...)
-- To pause a city: UPDATE ... SET active = FALSE WHERE city = '...'
INSERT INTO `fg-polylabs.weather.tracked_cities` (city, display_name, timezone, active, added_date)
VALUES
  ('london',        'London',        'Europe/London',                   TRUE, '2026-01-01'),
  ('singapore',     'Singapore',     'Asia/Singapore',                  TRUE, '2026-01-01'),
  ('paris',         'Paris',         'Europe/Paris',                    TRUE, '2026-02-01'),
  ('tokyo',         'Tokyo',         'Asia/Tokyo',                      TRUE, '2026-02-01'),
  ('nyc',           'New York',      'America/New_York',                TRUE, '2026-02-01'),
  ('chicago',       'Chicago',       'America/Chicago',                 TRUE, '2026-02-01'),
  ('miami',         'Miami',         'America/New_York',                TRUE, '2026-02-01'),
  ('dallas',        'Dallas',        'America/Chicago',                 TRUE, '2026-02-01'),
  ('toronto',       'Toronto',       'America/Toronto',                 TRUE, '2026-02-01'),
  ('seoul',         'Seoul',         'Asia/Seoul',                      TRUE, '2026-02-03'),
  ('ankara',        'Ankara',        'Europe/Istanbul',                 TRUE, '2026-02-03'),
  ('buenos-aires',  'Buenos Aires',  'America/Argentina/Buenos_Aires',  TRUE, '2026-02-03');
