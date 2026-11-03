package db

import (
	"context"
	"fmt"
	"log"
)

// requiredColumn 是一条「启动时必须存在」的字段契约。
//
// schema.sql 每次启动都会整体 Exec（幂等），CREATE TABLE 里带新列、ALTER ... ADD
// COLUMN IF NOT EXISTS 补旧库，理论上已经覆盖了它。这份清单是第二道保险：
//
//   - schema.sql 的 DDL 一旦漏写或被截断，服务不会带着缺列的库跑起来；
//   - 某个部署的库停在更早的版本、且 ALTER 恰好没有覆盖到，这里会补上；
//   - 日志里能直接看到「补了哪一列」，升级后不用猜这次动过什么。
//
// 加新列时两处都要登记：schema.sql（CREATE + ALTER 补旧库）与本清单。
// 漏登记不会立刻出错——schema.sql 大概率已经建好了——但下一次真出问题时，
// 这道保险就不存在了。
type requiredColumn struct {
	Table  string
	Column string
	DDL    string
}

// requiredColumns 是全部契约列的登记表。顺序即补充顺序，父表列在前。
var requiredColumns = []requiredColumn{
	{
		Table:  "llm_profiles",
		Column: "max_concurrency",
		DDL:    `ALTER TABLE llm_profiles ADD COLUMN IF NOT EXISTS max_concurrency INTEGER NOT NULL DEFAULT 0 CHECK (max_concurrency >= 0)`,
	},
	{
		Table:  "llm_profiles",
		Column: "ultra",
		DDL:    `ALTER TABLE llm_profiles ADD COLUMN IF NOT EXISTS ultra BOOLEAN NOT NULL DEFAULT false`,
	},
}

// EnsureSchemaColumns 在 schema 应用之后核对字段契约，缺什么补什么。
//
// 任何一步失败都返回错误，让 Open 直接失败：带着缺列的库起服务，错误会推迟到
// 运行期某条 SELECT 上才炸（"column X does not exist"），那时候更难定位。
// 启动即失败至少指得清是哪张表哪一列。
func (d *DB) EnsureSchemaColumns(ctx context.Context) error {
	for _, c := range requiredColumns {
		var exists bool
		if err := d.QueryRowContext(ctx, `
SELECT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = current_schema()
      AND table_name = $1
      AND column_name = $2
)`, c.Table, c.Column).Scan(&exists); err != nil {
			return fmt.Errorf("检查字段 %s.%s: %w", c.Table, c.Column, err)
		}
		if exists {
			continue
		}
		if _, err := d.ExecContext(ctx, c.DDL); err != nil {
			return fmt.Errorf("补充字段 %s.%s: %w", c.Table, c.Column, err)
		}
		log.Printf("[schema] 检测到缺失字段 %s.%s，已自动补充", c.Table, c.Column)
	}
	return nil
}
