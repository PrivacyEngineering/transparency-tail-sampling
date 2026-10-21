"""Minimal stand-in for the demo's featureflagservice (Elixir + Postgres), which is not deployed in the
trimmed setup. Answers every GetFlag with enabled=false (= all failure-injection flags off, the demo default).
Runs inside the recommendationservice image, which already ships grpcio and the generated demo stubs."""
from concurrent import futures
import grpc
import demo_pb2, demo_pb2_grpc

class Stub(demo_pb2_grpc.FeatureFlagServiceServicer):
    def GetFlag(self, request, context):
        return demo_pb2.GetFlagResponse(flag=demo_pb2.Flag(name=request.name, description="stub", enabled=False))

server = grpc.server(futures.ThreadPoolExecutor(max_workers=4))
demo_pb2_grpc.add_FeatureFlagServiceServicer_to_server(Stub(), server)
server.add_insecure_port("[::]:50053")
server.start()
print("featureflag stub listening on :50053", flush=True)
server.wait_for_termination()
