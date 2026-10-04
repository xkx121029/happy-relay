package store

import (
	"path/filepath"
	"testing"
)

func newCacheTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatalf("开库失败：%v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// TestModelCacheInvalidation 缓存一致性：写操作后必须立刻能读到新值。
// 这是加缓存最容易出错的地方——版本号漏打一次，模型就"改了不生效"。
func TestModelCacheInvalidation(t *testing.T) {
	s := newCacheTestStore(t)
	prov, err := s.SaveProvider(Provider{Name: "p1", Kind: KindOpenAI, BaseURLs: []string{"https://a.example.com/v1"}, APIKeys: []string{"sk-a"}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.SaveModel(Model{Name: "m1", UpstreamName: "u1", Capability: CapChat, Enabled: true,
		Inputs: []ModelInput{{ProviderID: prov.ID, UpstreamName: "u1"}}})
	if err != nil {
		t.Fatal(err)
	}
	// 预热缓存
	if _, err := s.ResolveModel("m1"); err != nil {
		t.Fatalf("预热后应能解析：%v", err)
	}
	if _, err := s.ListModels(); err != nil {
		t.Fatal(err)
	}

	t.Run("改模型名后旧名查不到、新名能查到", func(t *testing.T) {
		m.Name = "m2"
		if _, err := s.SaveModel(m); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ResolveModel("m1"); err == nil {
			t.Fatal("旧名不该再解析成功（缓存没失效）")
		}
		if _, err := s.ResolveModel("m2"); err != nil {
			t.Fatalf("新名应能解析：%v", err)
		}
	})

	t.Run("停用后不应出现在解析里", func(t *testing.T) {
		cur, err := s.GetModel(m.ID)
		if err != nil {
			t.Fatal(err)
		}
		cur.Enabled = false
		if _, err := s.SaveModel(cur); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ResolveModel("m2"); err == nil {
			t.Fatal("停用模型不该解析成功")
		}
	})

	t.Run("删模型后列表里不再出现", func(t *testing.T) {
		cur, err := s.GetModel(m.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteModel(cur.ID); err != nil {
			t.Fatal(err)
		}
		list, err := s.ListModels()
		if err != nil {
			t.Fatal(err)
		}
		for _, x := range list {
			if x.Name == "m2" {
				t.Fatal("删除后列表仍含该模型（缓存没失效）")
			}
		}
	})

	t.Run("新增模型立刻可解析", func(t *testing.T) {
		if _, err := s.SaveModel(Model{Name: "m3", UpstreamName: "u3", Capability: CapChat, Enabled: true,
			Inputs: []ModelInput{{ProviderID: prov.ID, UpstreamName: "u3"}}}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ResolveModel("m3"); err != nil {
			t.Fatalf("新建模型应立刻可解析：%v", err)
		}
	})

	t.Run("删渠道后模型列表同步更新", func(t *testing.T) {
		if _, err := s.ListModels(); err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteProvider(prov.ID); err != nil {
			t.Fatal(err)
		}
		list, err := s.ListModels()
		if err != nil {
			t.Fatal(err)
		}
		for _, x := range list {
			for _, in := range x.Inputs {
				if in.ProviderID == prov.ID {
					t.Fatal("渠道已删但模型仍绑定它（缓存没失效）")
				}
			}
		}
	})
}

// TestCatalogContextCacheInvalidation 目录写操作后上下文索引必须更新。
func TestCatalogContextCacheInvalidation(t *testing.T) {
	s := newCacheTestStore(t)
	prov, err := s.SaveProvider(Provider{Name: "p1", Kind: KindOpenAI, BaseURLs: []string{"https://a.example.com/v1"}, APIKeys: []string{"sk-a"}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveModel(Model{Name: "m1", UpstreamName: "u1", Capability: CapChat, Enabled: true,
		Inputs: []ModelInput{{ProviderID: prov.ID, UpstreamName: "u1"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveCatalogModel(CatalogModel{ProviderID: prov.ID, ModelID: "u1", Capability: CapChat, ContextLength: 32768}); err != nil {
		t.Fatal(err)
	}
	got, err := s.ResolveModel("m1")
	if err != nil {
		t.Fatal(err)
	}
	if got.EffectiveContextLength() != 32768 {
		t.Fatalf("应读到目录里的 32768，实际 %d", got.EffectiveContextLength())
	}

	// 改目录里的上下文长度 → 必须立刻生效
	if _, err := s.SaveCatalogModel(CatalogModel{ProviderID: prov.ID, ModelID: "u1", Capability: CapChat, ContextLength: 65536}); err != nil {
		t.Fatal(err)
	}
	got, err = s.ResolveModel("m1")
	if err != nil {
		t.Fatal(err)
	}
	if got.EffectiveContextLength() != 65536 {
		t.Fatalf("目录更新后未生效，实际 %d", got.EffectiveContextLength())
	}
}

// TestProviderPartialCache 局部缓存不能把"没缓存"误判成"渠道不存在"。
// GetProvider 命中过一次后会局部回填快照，但那份快照并不完整；
// 此时查另一个渠道必须照常返回结果，而不是 ErrNotFound。
func TestProviderPartialCache(t *testing.T) {
	s := newCacheTestStore(t)
	var ids []int64
	for i := 0; i < 3; i++ {
		p, err := s.SaveProvider(Provider{
			Name: "p" + string(rune('a'+i)), Kind: KindOpenAI,
			BaseURLs: []string{"https://a.example.com/v1"}, APIKeys: []string{"sk-a"}, Enabled: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, p.ID)
	}
	// 只查第一个（触发局部回填），再查其余的
	if _, err := s.GetProvider(ids[0]); err != nil {
		t.Fatalf("查渠道 1 失败：%v", err)
	}
	for _, id := range ids[1:] {
		if _, err := s.GetProvider(id); err != nil {
			t.Fatalf("局部缓存后查渠道 %d 被误判为不存在：%v", id, err)
		}
	}
	// 完整快照建立后，仍要能查到全部
	if _, err := s.ListProviders(); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if _, err := s.GetProvider(id); err != nil {
			t.Fatalf("完整快照后查渠道 %d 失败：%v", id, err)
		}
	}
	// 删掉一个渠道后，缓存必须失效
	if err := s.DeleteProvider(ids[2]); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetProvider(ids[2]); err == nil {
		t.Fatal("已删渠道在缓存失效后仍能查到")
	}
}
