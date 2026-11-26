CREATE TABLE "comments" (
  "id" TEXT PRIMARY KEY,
  "created_at" INTEGER NOT NULL,
  "updated_at" INTEGER NOT NULL,
  "version" INTEGER NOT NULL DEFAULT 1,
  "post_id" TEXT NOT NULL REFERENCES "posts"("id") ON DELETE SET NULL,
  "body" TEXT NOT NULL,
  "author_id" TEXT REFERENCES "_dk_users"("id") ON DELETE SET NULL
) STRICT;

CREATE INDEX "idx_comments_post_id_created_at" ON "comments" ("post_id", "created_at");

CREATE TABLE "posts" (
  "id" TEXT PRIMARY KEY,
  "created_at" INTEGER NOT NULL,
  "updated_at" INTEGER NOT NULL,
  "version" INTEGER NOT NULL DEFAULT 1,
  "title" TEXT NOT NULL CHECK (length("title") BETWEEN 1 AND 200),
  "slug" TEXT NOT NULL UNIQUE,
  "body" TEXT,
  "published" INTEGER DEFAULT 0 CHECK ("published" IN (0, 1)),
  "author_id" TEXT REFERENCES "_dk_users"("id") ON DELETE SET NULL,
  "status" TEXT CHECK ("status" IN ('draft', 'published')),
  "metadata" TEXT
) STRICT;

CREATE INDEX "idx_posts_published_created_at" ON "posts" ("published", "created_at");

CREATE UNIQUE INDEX "posts_slug_idx" ON "posts" ("slug");

CREATE INDEX "idx_posts_author_id" ON "posts" ("author_id") WHERE published = 1;

CREATE TABLE "profiles" (
  "id" TEXT PRIMARY KEY,
  "created_at" INTEGER NOT NULL,
  "updated_at" INTEGER NOT NULL,
  "version" INTEGER NOT NULL DEFAULT 1,
  "email" TEXT NOT NULL UNIQUE,
  "name" TEXT
) STRICT;
