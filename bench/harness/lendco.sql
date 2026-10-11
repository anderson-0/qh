-- lendco: Pagila's data under names no model has memorized, so a benchmark
-- question cannot be answered from training data and the agent must
-- discover the schema. Runs after pagila-schema.sql and pagila-data.sql;
-- leaves nothing of Pagila (no views, functions, triggers or old types).
CREATE SCHEMA lend;
CREATE TYPE lend.audience_band AS ENUM ('G', 'PG', 'PG-13', 'R', 'NC-17');

CREATE TABLE lend.nation AS SELECT country_id AS nation_key, country AS nation_nm FROM public.country;
CREATE TABLE lend.locality AS SELECT city_id AS locality_key, city AS locality_nm, country_id AS nation_key FROM public.city;
CREATE TABLE lend.residence AS
  SELECT address_id AS residence_key, address AS street_line, address2 AS street_line2, district AS district_nm,
         city_id AS locality_key, postal_code AS postcode, phone AS phone_no
  FROM public.address;
CREATE TABLE lend.lang AS SELECT language_id AS lang_key, trim(name) AS lang_label FROM public.language;
CREATE TABLE lend.genre AS SELECT category_id AS genre_key, name AS genre_label FROM public.category;
CREATE TABLE lend.performer AS SELECT actor_id AS performer_key, first_name AS given_nm, last_name AS family_nm FROM public.actor;
CREATE TABLE lend.catalog_item AS
  SELECT film_id AS item_key, title AS item_title, description AS synopsis, release_year::int AS release_yr,
         language_id AS lang_key, rental_duration AS loan_days, rental_rate AS loan_fee, length AS runtime_min,
         replacement_cost AS replacement_fee, rating::text::lend.audience_band AS audience, special_features AS extras
  FROM public.film;
CREATE TABLE lend.item_cast AS SELECT film_id AS item_key, actor_id AS performer_key FROM public.film_actor;
CREATE TABLE lend.item_genre AS SELECT film_id AS item_key, category_id AS genre_key FROM public.film_category;
CREATE TABLE lend.employee AS
  SELECT staff_id AS employee_key, first_name AS given_nm, last_name AS family_nm, address_id AS residence_key,
         email AS contact_email, store_id AS branch_key, active AS is_active, username AS login_nm
  FROM public.staff;
CREATE TABLE lend.branch AS SELECT store_id AS branch_key, manager_staff_id AS supervisor_key, address_id AS residence_key FROM public.store;
CREATE TABLE lend.member AS
  SELECT customer_id AS member_key, store_id AS branch_key, first_name AS given_nm, last_name AS family_nm,
         email AS contact_email, address_id AS residence_key, activebool AS is_active, create_date AS joined_on
  FROM public.customer;
CREATE TABLE lend.stock_unit AS SELECT inventory_id AS unit_key, film_id AS item_key, store_id AS branch_key FROM public.inventory;
CREATE TABLE lend.loan AS
  SELECT rental_id AS loan_key, rental_date AS loaned_at, inventory_id AS unit_key, customer_id AS member_key,
         return_date AS returned_at, staff_id AS employee_key
  FROM public.rental;
CREATE TABLE lend.charge AS
  SELECT payment_id AS charge_key, customer_id AS member_key, staff_id AS employee_key, rental_id AS loan_key,
         amount AS amt, payment_date AS charged_at
  FROM public.payment;

DROP SCHEMA public CASCADE;
ALTER SCHEMA lend RENAME TO public;

ALTER TABLE nation ADD PRIMARY KEY (nation_key);
ALTER TABLE locality ADD PRIMARY KEY (locality_key), ADD FOREIGN KEY (nation_key) REFERENCES nation;
ALTER TABLE residence ADD PRIMARY KEY (residence_key), ADD FOREIGN KEY (locality_key) REFERENCES locality;
ALTER TABLE lang ADD PRIMARY KEY (lang_key);
ALTER TABLE genre ADD PRIMARY KEY (genre_key);
ALTER TABLE performer ADD PRIMARY KEY (performer_key);
ALTER TABLE catalog_item ADD PRIMARY KEY (item_key), ADD FOREIGN KEY (lang_key) REFERENCES lang;
ALTER TABLE item_cast ADD PRIMARY KEY (item_key, performer_key),
  ADD FOREIGN KEY (item_key) REFERENCES catalog_item, ADD FOREIGN KEY (performer_key) REFERENCES performer;
ALTER TABLE item_genre ADD PRIMARY KEY (item_key, genre_key),
  ADD FOREIGN KEY (item_key) REFERENCES catalog_item, ADD FOREIGN KEY (genre_key) REFERENCES genre;
ALTER TABLE branch ADD PRIMARY KEY (branch_key), ADD FOREIGN KEY (residence_key) REFERENCES residence;
ALTER TABLE employee ADD PRIMARY KEY (employee_key),
  ADD FOREIGN KEY (residence_key) REFERENCES residence, ADD FOREIGN KEY (branch_key) REFERENCES branch;
ALTER TABLE branch ADD FOREIGN KEY (supervisor_key) REFERENCES employee;
ALTER TABLE member ADD PRIMARY KEY (member_key),
  ADD FOREIGN KEY (branch_key) REFERENCES branch, ADD FOREIGN KEY (residence_key) REFERENCES residence;
ALTER TABLE stock_unit ADD PRIMARY KEY (unit_key),
  ADD FOREIGN KEY (item_key) REFERENCES catalog_item, ADD FOREIGN KEY (branch_key) REFERENCES branch;
ALTER TABLE loan ADD PRIMARY KEY (loan_key), ADD FOREIGN KEY (unit_key) REFERENCES stock_unit,
  ADD FOREIGN KEY (member_key) REFERENCES member, ADD FOREIGN KEY (employee_key) REFERENCES employee;
ALTER TABLE charge ADD PRIMARY KEY (charge_key), ADD FOREIGN KEY (member_key) REFERENCES member,
  ADD FOREIGN KEY (employee_key) REFERENCES employee, ADD FOREIGN KEY (loan_key) REFERENCES loan;
CREATE INDEX ON loan (member_key);
CREATE INDEX ON charge (member_key);
CREATE INDEX ON stock_unit (item_key);
ANALYZE;
