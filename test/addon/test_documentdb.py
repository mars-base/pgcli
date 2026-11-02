#!/usr/bin/env python3
"""Smoke-test a DocumentDB gateway over the MongoDB wire protocol."""

import os
from urllib.parse import quote

from pymongo import MongoClient

host = os.environ.get("MONGO_HOST", "192.0.2.10")
port = os.environ.get("MONGO_PORT", "10260")
user = os.environ.get("MONGO_USER", "docdb_admin")
password = quote(os.environ["MONGO_PASSWORD"], safe="")

# 设置 MONGO_TLS=true 以启用 TLS。
use_tls = os.environ.get("MONGO_TLS", "").lower() in ("1", "true", "yes")
params = "directConnection=true"
if use_tls:
    params += "&tls=true&tlsAllowInvalidCertificates=true"

uri = f"mongodb://{user}:{password}@{host}:{port}/?{params}"
client = MongoClient(uri, serverSelectionTimeoutMS=10000)

print("ping:", client.admin.command("ping"))

col = client["pgcli_e2e"]["smoke"]
col.drop()
print("inserted:", col.insert_one({"name": "alpha", "value": 1}).inserted_id)
print("found:   ", col.find_one({"name": "alpha"}))
print("updated: ", col.update_one({"name": "alpha"}, {"$set": {"value": 2}}).modified_count)
print("index:   ", col.create_index([("value", 1)]))
print("count:   ", col.count_documents({}))
print("deleted: ", col.delete_one({"name": "alpha"}).deleted_count)

client.drop_database("pgcli_e2e")
client.close()
print("OK")
