package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

type HLSArtifact struct {
	ent.Schema
}

func (HLSArtifact) Fields() []ent.Field {
	return []ent.Field{
		field.Int("source_file_id"),
		field.String("storage_path"),
		field.Int("segment_count").
			Default(0),
		field.Int64("total_size").
			Default(0),
		field.String("codec").
			Default("h264/aac"),
	}
}

func (HLSArtifact) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("source_file", File.Type).
			Ref("hls_artifact").
			Field("source_file_id").
			Unique().
			Required(),
	}
}
