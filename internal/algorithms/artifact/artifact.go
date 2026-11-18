// Package artifact is the host facade for the public external artifact SDK.
package artifact

import public "github.com/DavidLiu-code/SeisForge-Studio/pkg/algorithmapi/artifact"

const Version = public.Version
const Float32 = public.Float32
const Float64 = public.Float64

type Metadata = public.Metadata
type TraceMetadata = public.TraceMetadata
type ReadTraceFunc = public.ReadTraceFunc
type File = public.File

var Write = public.Write
var Open = public.Open
var OpenContext = public.OpenContext
var CheckCompatibility = public.CheckCompatibility
