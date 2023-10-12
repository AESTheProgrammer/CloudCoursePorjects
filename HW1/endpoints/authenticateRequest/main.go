package main

import (
	"context"
	"encoding/json"
	"io/ioutil"
	"log"
	"net/http"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dynamotypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ses"
)

type Form struct {
	Username   string `dynamodbav:"username" binding:"reuired"`
	Email      string `dynamodbav:"email" binding:"reuired,email"`
	LastName   string `dynamodbav:"lastName" binding:"reuired,min=5,max=15"`
	NationalID int    `dynamodbav:"nationalID" binding:"required,min=0,max=100000"`
}

type User struct {
	Username   string `dynamodbav:"username"`
	Email      string `dynamodbav:"email"`
	LastName   string `dynamodbav:"lastName"`
	NationalID int    `dynamodbav:"nationalID"`
	IP         string `dynamodbav:"ip"`
	Image1     string `dynamodbav:"image1"`
	Image2     string `dynamodbav:"image2"`
	State      string `dynamodbav:"state"`
}

type Coordinates struct {
	Height int `json:"height"`
	Width  int `json:"width"`
	XMax   int `json:"xmax"`
	XMin   int `json:"xmin"`
	YMax   int `json:"ymax"`
	YMin   int `json:"ymin"`
}

type Face struct {
	Confidence  float64     `json:"confidence"`
	Coordinates Coordinates `json:"coordinates"`
	FaceID      string      `json:"face_id"`
}

type Result struct {
	Faces []Face `json:"faces"`
}

type Status struct {
	Text string `json:"text"`
	Type string `json:"type"`
}

type DetectionResponse struct {
	Result Result `json:"result"`
	Status Status `json:"status"`
}

type SimilarityResponse struct {
	Result struct {
		Score float64 `json:"score"`
	} `json:"result"`
	Status Status `json:"status"`
}

type errorString struct {
	s string
}

const (
	api_key    = "acc_7d54f0cae7319d2"
	api_secret = "d4a4b8a3f75b7539bef24cd181f070d0"
	TableName  = "BankingAuthenticationService"
	CharSet    = "UTF-8"
	Subject    = "Banking Authentication"
	Sender     = "armines.bin2000@gmail.com"
)

var (
	dbClient  dynamodb.Client
	sdkConfig aws.Config
)

func main() {
	lambda.Start(Handler)
}

func init() {
	sdkConfig, err := config.LoadDefaultConfig(context.TODO())
	if err != nil {
		log.Fatal(err)
	}
	dbClient = *dynamodb.NewFromConfig(sdkConfig)
}

func (e *errorString) Error() string {
	return e.s
}

func Handler(ctx context.Context, sqsEvent events.SQSEvent) error {
	for _, message := range sqsEvent.Records {
		log.Printf("The message %s for event source %s = %s \n", message.MessageId, message.EventSource, message.Body)
		user, err := GetUserInfo(message.Body)
		if err != nil {
			log.Printf("Error, getUserInfo, ID: %v\n", message.MessageId)
			continue
		}
		score, err := ProcessImages(user.Image1, user.Image2)
		if err != nil {
			log.Printf("Error, processImages, ID: %v\n", message.MessageId)
			continue
		}
		err = sendEmail(score, user.Email)
		if err != nil {
			log.Printf("Error, sendEmail, ID: %v\n", message.MessageId)
		}
	}
	return nil
}

func sendEmail(score int, recipient string) error {
	msg := "Authenticaion was failed."
	if score >= 80 {
		msg = "Authenticaion was successful."
	}
	sess, err := session.NewSession(&sdkConfig)
	if err != nil {
		log.Printf("Couldn't build a new session. Here's why: %v\n", err)
		return err
	}
	svc := ses.New(sess)
	input := &ses.SendEmailInput{
		Destination: &ses.Destination{
			CcAddresses: []*string{},
			ToAddresses: []*string{
				aws.String(recipient),
			},
		},
		Message: &ses.Message{
			Body: &ses.Body{
				Text: &ses.Content{
					Charset: aws.String(CharSet),
					Data:    aws.String(msg),
				},
			},
			Subject: &ses.Content{
				Charset: aws.String(CharSet),
				Data:    aws.String(Subject),
			},
		},
		Source: aws.String(Sender),
	}
	result, err := svc.SendEmail(input)
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			switch aerr.Code() {
			case ses.ErrCodeMessageRejected:
				log.Println(ses.ErrCodeMessageRejected, aerr.Error())
			case ses.ErrCodeMailFromDomainNotVerifiedException:
				log.Println(ses.ErrCodeMailFromDomainNotVerifiedException, aerr.Error())
			case ses.ErrCodeConfigurationSetDoesNotExistException:
				log.Println(ses.ErrCodeConfigurationSetDoesNotExistException, aerr.Error())
			default:
				log.Println(aerr.Error())
			}
		} else {
			log.Println(err.Error())
		}
		return err
	}
	log.Println("Email Sent to address: " + recipient)
	log.Println(result)
	return nil
}

func compareFaces(faceID1 string, faceID2 string) (int, error) {
	client := &http.Client{}
	req, _ := http.NewRequest("GET", "https://api.imagga.com/v2/faces/similarity?face_id="+faceID1+"&second_face_id="+faceID2, nil)
	req.SetBasicAuth(api_key, api_secret)
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("Error when sending request to the server")
		return 0, err
	}
	defer resp.Body.Close()
	resp_body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Printf("Error while reading response body. Here's why: %v\n", err)
		return 0, err
	}
	var simResp SimilarityResponse
	err = json.Unmarshal(resp_body, &simResp)
	if err != nil {
		log.Printf("Couldn't decode response body. Here's why: %v\n", err)
		return 0, err
	}
	return int(simResp.Result.Score), nil
}

func DetectFace(url string) (string, error) {
	client := &http.Client{}
	req, _ := http.NewRequest("GET", "https://api.imagga.com/v2/faces/detections?image_url="+url+
		"&return_face_id=1", nil)
	req.SetBasicAuth(api_key, api_secret)
	resp, err := client.Do(req)
	if err != nil {
		log.Println("Error when sending request to the server.")
		return "", err
	}
	defer resp.Body.Close()
	resp_body, _ := ioutil.ReadAll(resp.Body)
	var detResp DetectionResponse
	err = json.Unmarshal(resp_body, &detResp)
	if err != nil {
		log.Printf("Couldn't decode response body. Here's why: %v\n", err)
		return "", err
	}
	if detResp.Status.Type != "success" {
		log.Printf("Couldn't detect face at %v: %v\n", url, detResp.Status.Type)
		return "", &errorString{"Couldn't detect face."}
	}
	if len(detResp.Result.Faces) > 1 {
		log.Printf("detected more than 1 face: %v\n", len(detResp.Result.Faces))
		return "", &errorString{"detected more than 1 face"}
	}
	log.Printf("status: %v\nfaceid: %v\n ", detResp.Status.Type, detResp.Result.Faces[0].FaceID)
	return detResp.Result.Faces[0].FaceID, nil
}

func ProcessImages(url1 string, url2 string) (int, error) {
	faceID1, err := DetectFace(url1)
	if err != nil {
		return 0, err
	}
	faceID2, err := DetectFace(url2)
	if err != nil {
		return 0, err
	}
	score, err := compareFaces(faceID1, faceID2)
	if err != nil {
		return 0, err
	}
	return score, nil
}

func GetUserInfo(username string) (*User, error) {
	key, err := attributevalue.Marshal(username)
	if err != nil {
		log.Printf("Couldn't marshal nationalID. Here's why: %v\n", err)
		return nil, err
	}
	result, err := dbClient.GetItem(context.TODO(), &dynamodb.GetItemInput{
		TableName: aws.String(TableName),
		Key: map[string]dynamotypes.AttributeValue{
			"username": key,
		},
	})
	if err != nil {
		log.Printf("Couldn't retrieve record from dynamodb. Here's why: %v\n", err)
		return nil, err
	}
	var user User
	err = attributevalue.UnmarshalMap(result.Item, &user)
	if err != nil {
		log.Printf("Couldn't unmarshal resposne from dynamodb. Here's why: %v\n", err)
		return nil, err
	}
	return &user, nil
}